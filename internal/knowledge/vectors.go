// vectors.go - dense vector storage for hybrid retrieval (HR-004).
//
// Vectors live in sidecars (<knowledgedir>/vectors/<slug>.vec.json), never
// in the notes themselves: files stay the source of truth, and the note
// index fingerprint (which counts only *.md) is unaffected. Validity is
// lazy and per-note — a stale sidecar (edited note, changed model or
// dimension) is re-embedded on the next hybrid search, never trusted.

package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

// EmbedFn embeds texts into dense vectors, one per input, in input order.
// It is built in setupRunner from the embed config over
// OpenAIProvider.EmbedTexts. nil means hybrid retrieval is unavailable
// (tests, CLI without config, sleep) and search degrades to BM25-only.
var EmbedFn func(ctx context.Context, texts []string) ([][]float32, error)

// docTextRunes caps the embedded document text. Front-loaded: the fields
// BM25 already boosts come first so truncation drops body tail, not signal.
const docTextRunes = 2000

// docText renders a note as the single document text its vector represents:
// title + aliases + summary + body, truncated to docTextRunes runes.
func docText(n *KnowledgeNote) string {
	var b strings.Builder
	b.WriteString(n.Frontmatter.Title)
	b.WriteString("\n")
	if len(n.Frontmatter.Aliases) > 0 {
		b.WriteString(strings.Join(n.Frontmatter.Aliases, " "))
		b.WriteString("\n")
	}
	if n.Frontmatter.Summary != "" {
		b.WriteString(n.Frontmatter.Summary)
		b.WriteString("\n")
	}
	b.WriteString(n.Body)
	s := b.String()
	if utf8.RuneCountInString(s) <= docTextRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:docTextRunes])
}

// vectorSidecar is the on-disk shape of vectors/<slug>.vec.json. noteSize
// + noteMtime pin the note revision the vector was computed from; model +
// dim pin the embedding configuration. Any mismatch invalidates.
type vectorSidecar struct {
	Model     string    `json:"model"`
	Dim       int       `json:"dim"`
	Vector    []float32 `json:"vector"`
	NoteSize  int64     `json:"note_size"`
	NoteMtime int64     `json:"note_mtime_unix_nano"`
}

// vectorsDir returns the sidecar directory for a resolved knowledge dir.
func vectorsDir(dir string) string {
	return filepath.Join(KnowledgeDir(dir), "vectors")
}

// vectorPath returns the sidecar path for a slug. Slugs come from note
// filenames; the separator guard keeps a hostile filename from escaping
// the vectors directory.
func vectorPath(dir, slug string) (string, error) {
	if slug == "" || slug != filepath.Base(slug) {
		return "", fmt.Errorf("invalid slug %q", slug)
	}
	return filepath.Join(vectorsDir(dir), slug+".vec.json"), nil
}

// readVectorSidecar loads and parses a sidecar. Missing file is (nil, nil);
// corrupt JSON is an error so callers re-embed rather than trust garbage.
func readVectorSidecar(path string) (*vectorSidecar, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var sc vectorSidecar
	if err := json.Unmarshal(raw, &sc); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &sc, nil
}

// writeVectorSidecar stores a sidecar atomically (tmp+rename, mirroring
// SaveNote).
func writeVectorSidecar(path string, sc *vectorSidecar) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(sc)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// sidecarValid reports whether sc matches the note file at notePath under
// the given model/dim. Every field must agree; doubt means re-embed.
func sidecarValid(sc *vectorSidecar, notePath, model string, dim int) bool {
	if sc == nil || sc.Model != model || sc.Dim != dim {
		return false
	}
	if len(sc.Vector) == 0 || len(sc.Vector) != dim {
		return false
	}
	st, err := os.Stat(notePath)
	if err != nil {
		return false
	}
	return sc.NoteSize == st.Size() && sc.NoteMtime == st.ModTime().UnixNano()
}

// cachedVector pairs a vector with the note revision it was computed from,
// so warm searches validate with one stat instead of re-reading sidecars.
type cachedVector struct {
	vec   []float32
	size  int64
	mtime int64
}

// vectorCacheEntry pairs cached vectors with the model/dim they belong to.
type vectorCacheEntry struct {
	model string
	dim   int
	vecs  map[string]cachedVector
}

// vectorCache caches one vector map per resolved knowledge directory.
// Safe for concurrent use (sync.Map).
var vectorCache sync.Map // resolved dir -> *vectorCacheEntry

// invalidateVectorCache drops cached vectors for a directory. Called from
// InvalidateKnowledgeCache so note writes also drop stale vectors.
func invalidateVectorCache(dir string) {
	vectorCache.Delete(KnowledgeDir(dir))
}

// ensureVectors returns a vector per note in idx, embedding whatever is
// missing or stale via EmbedFn in a single batched call. model names the
// embedding model (recorded in every sidecar for drift detection); dim <= 0
// means "take the dimension from the first response" (model upgrades that
// change dim invalidate every sidecar via the dim check, then re-embed).
// Returns the vectors keyed by slug and the effective dimension.
func ensureVectors(ctx context.Context, dir string, idx *KnowledgeIndex, model string, dim int) (map[string][]float32, int, error) {
	if EmbedFn == nil {
		return nil, 0, fmt.Errorf("embeddings unavailable (no embedding endpoint configured)")
	}
	dir = KnowledgeDir(dir)
	cached, _ := vectorCache.Load(dir)
	entry, _ := cached.(*vectorCacheEntry)
	if entry == nil || entry.model != model || (dim > 0 && entry.dim != dim) {
		entry = &vectorCacheEntry{model: model, dim: dim, vecs: make(map[string]cachedVector)}
	}

	var missing []string
	var missingNotes []*KnowledgeNote
	vecs := make(map[string][]float32, len(idx.BySlug))
	for _, note := range idx.BySlug {
		vpath, err := vectorPath(dir, note.Slug)
		if err != nil {
			continue // hostile slug: BM25 still covers it
		}
		notePath := NotePath(dir, note.Slug)
		st, serr := os.Stat(notePath)
		if serr != nil {
			continue // note vanished mid-search: skip it
		}
		wantDim := dim
		if wantDim <= 0 {
			wantDim = entry.dim
		}
		// Memory fast path: stat matches the cached revision.
		if cv, ok := entry.vecs[note.Slug]; ok && wantDim > 0 && len(cv.vec) == wantDim &&
			cv.size == st.Size() && cv.mtime == st.ModTime().UnixNano() {
			vecs[note.Slug] = cv.vec
			continue
		}
		sc, rerr := readVectorSidecar(vpath)
		if rerr == nil && sc != nil && wantDim <= 0 && sc.Model == model && sc.Dim > 0 {
			// Cold cache after restart: adopt the stored dimension so
			// valid sidecars are trusted without re-embedding.
			wantDim = sc.Dim
			entry.dim = sc.Dim
		}
		if rerr == nil && wantDim > 0 && sidecarValid(sc, notePath, model, wantDim) {
			vecs[note.Slug] = sc.Vector
			entry.vecs[note.Slug] = cachedVector{vec: sc.Vector, size: st.Size(), mtime: st.ModTime().UnixNano()}
			continue
		}
		missing = append(missing, docText(note))
		missingNotes = append(missingNotes, note)
	}

	if len(missing) > 0 {
		emb, err := EmbedFn(ctx, missing)
		if err != nil {
			return nil, 0, err
		}
		if len(emb) != len(missing) {
			return nil, 0, fmt.Errorf("embeddings returned %d vectors for %d texts", len(emb), len(missing))
		}
		if len(emb) > 0 && len(emb[0]) == 0 {
			return nil, 0, fmt.Errorf("embeddings returned an empty vector")
		}
		if dim <= 0 {
			dim = len(emb[0])
		}
		for i, note := range missingNotes {
			if len(emb[i]) != dim {
				return nil, 0, fmt.Errorf("embeddings dimension drift within one call (%d vs %d)", len(emb[i]), dim)
			}
			vpath, err := vectorPath(dir, note.Slug)
			if err != nil {
				continue
			}
			notePath := NotePath(dir, note.Slug)
			st, err := os.Stat(notePath)
			if err != nil {
				continue // note vanished mid-search: skip it
			}
			_ = writeVectorSidecar(vpath, &vectorSidecar{ // best-effort: memory still serves this search
				Model:     model,
				Dim:       dim,
				Vector:    emb[i],
				NoteSize:  st.Size(),
				NoteMtime: st.ModTime().UnixNano(),
			})
			vecs[note.Slug] = emb[i]
			entry.vecs[note.Slug] = cachedVector{vec: emb[i], size: st.Size(), mtime: st.ModTime().UnixNano()}
		}
		entry.dim = dim
	}

	vectorCache.Store(dir, entry)
	return vecs, dim, nil
}

// cosine returns the cosine similarity of a and b (0 when either is zero
// or lengths mismatch — never NaN into the ranking).
func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
