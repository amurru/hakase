// artifact_save_test.go - AD-003 (T2.1): generated media is registered as a
// session artifact in addition to the disk store, and the registration is
// best-effort (never fails a generation).
package media

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/genai"
)

// recordingArtifacts captures saved artifacts.
type recordingArtifacts struct {
	saved map[string]*genai.Part
}

func (r *recordingArtifacts) Save(ctx context.Context, name string, data *genai.Part) (*artifact.SaveResponse, error) {
	if r.saved == nil {
		r.saved = map[string]*genai.Part{}
	}
	r.saved[name] = data
	return &artifact.SaveResponse{Version: 1}, nil
}

func (r *recordingArtifacts) List(context.Context) (*artifact.ListResponse, error) {
	return &artifact.ListResponse{}, nil
}

func (r *recordingArtifacts) Load(context.Context, string) (*artifact.LoadResponse, error) {
	return &artifact.LoadResponse{}, nil
}

func (r *recordingArtifacts) LoadVersion(context.Context, string, int) (*artifact.LoadResponse, error) {
	return &artifact.LoadResponse{}, nil
}

// saveCtx is an agent.Context with an optional artifact store.
type saveCtx struct {
	agent.StrictContextMock
	arts agent.Artifacts
}

func (c *saveCtx) Artifacts() agent.Artifacts { return c.arts }

func newSaveCtx(arts agent.Artifacts) *saveCtx {
	return &saveCtx{StrictContextMock: agent.NewStrictContextMock(context.Background()), arts: arts}
}

func TestSaveArtifactRegistersFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(path, []byte("PNGDATA"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	rec := &recordingArtifacts{}
	saveArtifact(newSaveCtx(rec), path, "image/png")

	part, ok := rec.saved["pic.png"]
	if !ok {
		t.Fatalf("artifact not saved, got %v", rec.saved)
	}
	if part.InlineData == nil || part.InlineData.MIMEType != "image/png" {
		t.Fatalf("part = %+v, want inline image/png", part)
	}
	if string(part.InlineData.Data) != "PNGDATA" {
		t.Errorf("data = %q, want PNGDATA", part.InlineData.Data)
	}
}

func TestSaveArtifactNilServiceIsNoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Must not panic when the session has no artifact service.
	saveArtifact(newSaveCtx(nil), path, "image/png")
	saveArtifact(nil, path, "image/png")
	saveArtifact(newSaveCtx(&recordingArtifacts{}), "", "image/png")
}

func TestSaveArtifactSkipsOversized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.bin")
	big := make([]byte, artifactSaveMaxBytes+1)
	if err := os.WriteFile(path, big, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	rec := &recordingArtifacts{}
	saveArtifact(newSaveCtx(rec), path, "application/octet-stream")
	if len(rec.saved) != 0 {
		t.Errorf("oversized artifact was saved: %v", rec.saved)
	}
}
