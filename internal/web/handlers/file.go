// Package handlers provides HTTP handlers for the hakase web API.
package handlers

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	hctx "amurru/hakase/internal/context"
	"amurru/hakase/internal/sandbox"
	"amurru/hakase/internal/vision"

	"github.com/google/uuid"
)

// maxFileContentSize caps text content returned by GET /api/files (1 MB).
const maxFileContentSize = 1 << 20

// maxImagePreviewSize caps image files for inline preview (10 MB).
const maxImagePreviewSize = 10 << 20

// maxDirEntries caps directory listing results.
const maxDirEntries = 1000

// maxBrowseFiles caps the bounded file walk for @ autocomplete.
const maxBrowseFiles = 500

// maxAttachmentImageBytes caps image attachments uploaded via the API (10 MB).
const maxAttachmentImageBytes = 10 << 20

// maxAttachmentTextBytes caps text file attachments (200 KB).
const maxAttachmentTextBytes = 200 * 1024

// FileRouter is the minimum interface needed by RegisterFileRoutes.
type FileRouter interface {
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
}

// FileAPI wraps file browsing operations for the web API layer.
type FileAPI struct{}

// RegisterFileRoutes registers file browsing API routes on the given router.
// Routes are relative to /api (the caller places them inside the /api group).
// Order matters: more specific routes first to avoid conflicts.
func RegisterFileRoutes(r FileRouter) {
	api := &FileAPI{}

	r.Get("/files/browse", api.BrowseFiles)
	r.Get("/files/list", api.ListDirectory)
	r.Get("/files/inline", api.InlineFile)
	r.Get("/files/download", api.DownloadFile)
	r.Get("/files/proxy", api.ProxyImage)
	r.Get("/files", api.ReadFile)
	r.Post("/sessions/{id}/attachments", api.UploadAttachment)
}

// fileEntry is a single entry in a directory listing response.
type fileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// fileContentResponse is the response for GET /api/files.
type fileContentResponse struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Content   string `json:"content,omitempty"`
	Size      int64  `json:"size"`
	Mime      string `json:"mime"`
	IsBinary  bool   `json:"is_binary"`
	Truncated bool   `json:"truncated,omitempty"`
}

// resolveFilePath resolves a path through the sandbox read roots.
// Returns the absolute resolved path or an error if outside the sandbox.
// Fails closed: when sandbox is not initialized or explicitly disabled,
// all file access is rejected. ResolveScopedPath (root-anchored, symlink-
// safe) is the containment boundary; the checks below fail fast on request
// shapes that could never resolve inside it.
func resolveFilePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if sandbox.CurrentSandbox == nil {
		return "", fmt.Errorf("sandbox is not initialized")
	}
	if sandbox.CurrentSandbox.Mode == sandbox.SandboxModeOff {
		return "", fmt.Errorf("sandbox is disabled; file access is not permitted")
	}
	if strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("path contains a NUL byte")
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the workspace")
	}
	return sandbox.CurrentSandbox.ResolveScopedPath(clean, false)
}

// isBinaryFile checks if data contains a null byte (simple binary detection).
func isBinaryFile(data []byte) bool {
	return len(data) > 0 && !utf8.Valid(data)
}

// detectMIME returns the MIME type for a file based on its extension.
func detectMIME(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return mimeType
}

// isImageMIME reports whether the MIME type is an image.
func isImageMIME(mimeType string) bool {
	return strings.HasPrefix(mimeType, "image/")
}

// ReadFile handles GET /api/files?path=<path> - returns file content.
// For text files, returns the content (capped at 1 MB).
// For images, returns metadata only (preview via download endpoint).
// For other binary files, returns metadata with is_binary=true.
func (api *FileAPI) ReadFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path parameter is required"})
		return
	}

	absPath, err := resolveFilePath(path)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": fmt.Sprintf("path outside workspace: %v", err)})
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to stat file: %v", err)})
		return
	}

	if info.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path is a directory, use /api/files/list"})
		return
	}

	name := info.Name()
	size := info.Size()
	mimeType := detectMIME(absPath)
	image := isImageMIME(mimeType)

	// For images, return metadata only (don't inline large binary data).
	if image {
		if size > maxImagePreviewSize {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "image too large for preview"})
			return
		}
		writeJSON(w, http.StatusOK, fileContentResponse{
			Path:     absPath,
			Name:     name,
			Size:     size,
			Mime:     mimeType,
			IsBinary: true,
		})
		return
	}

	// Read file content for text/binary detection.
	data, err := os.ReadFile(absPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to read file: %v", err)})
		return
	}

	binary := isBinaryFile(data)
	if binary {
		writeJSON(w, http.StatusOK, fileContentResponse{
			Path:     absPath,
			Name:     name,
			Size:     size,
			Mime:     mimeType,
			IsBinary: true,
		})
		return
	}

	// Text file: truncate if over limit.
	truncated := false
	content := string(data)
	if len(data) > maxFileContentSize {
		content = string(data[:maxFileContentSize])
		truncated = true
	}

	writeJSON(w, http.StatusOK, fileContentResponse{
		Path:      absPath,
		Name:      name,
		Content:   hctx.WrapUntrustedData(content),
		Size:      size,
		Mime:      mimeType,
		IsBinary:  false,
		Truncated: truncated,
	})
}

// DownloadFile handles GET /api/files/download?path=<path> - serves the file as attachment.
func (api *FileAPI) DownloadFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path parameter is required"})
		return
	}

	absPath, err := resolveFilePath(path)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": fmt.Sprintf("path outside workspace: %v", err)})
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to stat file: %v", err)})
		return
	}

	if info.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot download a directory"})
		return
	}

	mimeType := detectMIME(absPath)
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, info.Name()))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))

	f, err := os.Open(absPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to open file: %v", err)})
		return
	}
	defer f.Close()

	if _, err := io.Copy(w, f); err != nil {
		log.Printf("file: download stream error for %s: %v", absPath, err)
	}
}

// InlineFile handles GET /api/files/inline?path=<path> - serves a workspace
// file with Content-Disposition: inline for use as <img>/<video>/<audio>
// sources. Mirrors DownloadFile but uses inline disposition, and streams the
// whole file (no Range support in v1).
func (api *FileAPI) InlineFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "path parameter is required"})
		return
	}

	absPath, err := resolveFilePath(path)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": fmt.Sprintf("path outside workspace: %v", err)})
		return
	}

	// Open through a root-anchored descriptor operation to prevent symlink
	// traversal and pathname races. Open the file descriptor first, then
	// validate it's a regular file before serving.
	f, err := os.Open(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to open file: %v", err)})
		return
	}
	defer f.Close()

	// Get file info from the open descriptor (not the path) to avoid TOCTOU races.
	// Require it to be a regular file - reject directories, symlinks, FIFOs, sockets, etc.
	info, err := f.Stat()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to stat file: %v", err)})
		return
	}

	if info.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot inline a directory"})
		return
	}

	if !info.Mode().IsRegular() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot inline non-regular file"})
		return
	}

	mimeType := detectMIME(absPath)
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, info.Name()))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if _, err := io.Copy(w, f); err != nil {
		log.Printf("file: inline stream error for %s: %v", absPath, err)
	}
}

// maxProxyImageBytes caps images fetched through the proxy endpoint (10 MB),
// matching the vision tool's download ceiling.
const maxProxyImageBytes = 10 << 20

// proxyHTTPClient is the shared client for fetching remote images through
// /api/files/proxy. Redirect targets are re-checked against the SSRF guard at
// every hop so a public URL can never redirect to an internal address.
var proxyHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		MaxIdleConns:          8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		if err := vision.CheckHostPublic(req.URL.Host); err != nil {
			return fmt.Errorf("redirect target blocked: %w", err)
		}
		return nil
	},
}

// ProxyImage handles GET /api/files/proxy?url=<external_url> - fetches an
// image from an external http(s) URL and serves it inline from the agent
// server's own origin. This lets the web UI render remote photos under the
// strict Content-Security-Policy (img-src 'self') by proxying them through
// hakase, and gives the agent a deterministic way to show fetched images.
//
// The remote host is SSRF-guarded (private/loopback/link-local addresses are
// rejected, including at each redirect hop), the response is capped at
// maxProxyImageBytes, and only image MIME types are served. The bytes are
// streamed straight through with a Content-Type detected from the payload.
func (api *FileAPI) ProxyImage(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("url"))
	if raw == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url parameter is required"})
		return
	}

	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid url: must be an http(s) URL"})
		return
	}

	// SSRF guard: reject private, loopback, link-local, and unspecified
	// addresses before any request is made. Redirects are re-checked by
	// proxyHTTPClient.CheckRedirect.
	if err := vision.CheckHostPublic(u.Host); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": fmt.Sprintf("url blocked: %v", err)})
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid url"})
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) HakaseAgent/1.0")

	resp, err := proxyHTTPClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("fetch failed: %v", err)})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("upstream returned HTTP %d", resp.StatusCode)})
		return
	}

	// Stream through a limit reader to enforce the size cap.
	limited := io.LimitReader(resp.Body, maxProxyImageBytes+1)
	buf := make([]byte, 0, 64*1024)
	chunk := make([]byte, 32*1024)
	total := 0
	for {
		n, rerr := limited.Read(chunk)
		if n > 0 {
			total += n
			if total > maxProxyImageBytes {
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "image too large"})
				return
			}
			buf = append(buf, chunk[:n]...)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("read failed: %v", rerr)})
			return
		}
	}

	// Detect the MIME type from the payload (fall back to the upstream
	// Content-Type header). Only images are served - the proxy is not a
	// generic byte relay.
	mimeType, ok := vision.DetectImageMime(buf)
	if !ok {
		if upstream := resp.Header.Get("Content-Type"); upstream != "" && strings.HasPrefix(upstream, "image/") {
			mimeType = upstream
		} else {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url does not point to a recognizable image"})
			return
		}
	}

	filename := filepath.Base(u.Path)
	if filename == "" || filename == "." || filename == "/" {
		filename = "image"
	}
	if ext := mime.TypeByExtension(filepath.Ext(filename)); ext == "" {
		// Derive an extension from the detected MIME so the filename hint
		// is useful in Content-Disposition.
		if mimeType == "image/jpeg" {
			filename += ".jpg"
		} else if mimeType == "image/png" {
			filename += ".png"
		} else if mimeType == "image/webp" {
			filename += ".webp"
		} else if mimeType == "image/gif" {
			filename += ".gif"
		}
	}

	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(buf)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=3600")

	if _, err := w.Write(buf); err != nil {
		log.Printf("file: proxy stream error for %s: %v", raw, err)
	}
}

// ListDirectory handles GET /api/files/list?dir=<path> - lists directory contents.
// Returns entries sorted by name (dirs first), capped at maxDirEntries.
func (api *FileAPI) ListDirectory(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		// Default to the first read root.
		dir = "."
	}

	absPath, err := resolveFilePath(dir)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": fmt.Sprintf("path outside workspace: %v", err)})
		return
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "directory not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to read directory: %v", err)})
		return
	}

	// Hide deny-rooted entries (sensitive files like config.json) from
	// listings; reads of them are rejected by ResolveScopedPath anyway.
	var sb *sandbox.SandboxConfig = sandbox.CurrentSandbox

	result := make([]fileEntry, 0, len(entries))
	for _, entry := range entries {
		// Skip hidden files/dirs (starting with .).
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if sb.DeniedPath(filepath.Join(absPath, entry.Name())) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		result = append(result, fileEntry{
			Name:  entry.Name(),
			Path:  filepath.Join(absPath, entry.Name()),
			IsDir: entry.IsDir(),
			Size:  info.Size(),
		})

		if len(result) >= maxDirEntries {
			break
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// browseEntry is a single file entry in the browse autocomplete response.
type browseEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Mime string `json:"mime"`
}

// skippedDirNames lists directory basenames to skip during the bounded walk.
var skippedDirNames = map[string]bool{
	".git":          true,
	".hakase-tmp":   true,
	"node_modules":  true,
	"vendor":        true,
	"__pycache__":   true,
	".venv":         true,
	".omc":          true,
	".omo":          true,
}

// BrowseFiles handles GET /api/files/browse?q=<prefix> - bounded workspace
// file walk returning files matching the prefix. Used for @ autocomplete.
// Walks the first read root, skips hidden and heavy directories, caps at
// maxBrowseFiles results. Paths are returned relative to the workspace root.
func (api *FileAPI) BrowseFiles(w http.ResponseWriter, r *http.Request) {
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	root, err := resolveFilePath(".")
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": fmt.Sprintf("cannot resolve workspace: %v", err)})
		return
	}

	var results []browseEntry
	// Hide deny-rooted entries (sensitive files like config.json) from
	// autocomplete; reads of them are rejected by ResolveScopedPath anyway.
	var sb *sandbox.SandboxConfig = sandbox.CurrentSandbox
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if sb.DeniedPath(path) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != root {
				name := d.Name()
				if strings.HasPrefix(name, ".") || skippedDirNames[name] {
					return filepath.SkipDir
				}
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if query == "" || strings.Contains(strings.ToLower(rel), query) {
			mimeType := detectMIME(path)
			results = append(results, browseEntry{
				Name: d.Name(),
				Path: rel,
				Mime: mimeType,
			})
		}
		if len(results) >= maxBrowseFiles {
			return filepath.SkipAll
		}
		return nil
	})

	writeJSON(w, http.StatusOK, results)
}

// attachmentUploadResponse is the response for POST /api/sessions/{id}/attachments.
type attachmentUploadResponse struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	MIME  string `json:"mime"`
	Label string `json:"label"`
}

// UploadAttachment handles POST /api/sessions/{id}/attachments.
// Accepts multipart/form-data (file field) or JSON with name/mime/base64.
// Saves the file to .hakase-tmp/attachments/ and returns an AttachmentRef.
func (api *FileAPI) UploadAttachment(w http.ResponseWriter, r *http.Request) {
	var (
		data     []byte
		name     string
		mimeType string
	)

	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		// Limit body to 10 MB + overhead for form fields.
		r.Body = http.MaxBytesReader(w, r.Body, int64(maxAttachmentImageBytes)+1024*1024)
		if err := r.ParseMultipartForm(int64(maxAttachmentImageBytes) + 1024*1024); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to parse multipart form"})
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing 'file' field in form"})
			return
		}
		defer file.Close()
		name = header.Filename
		mimeType = header.Header.Get("Content-Type")
		if mimeType == "" {
			mimeType = detectMIME(name)
		}
		data, err = io.ReadAll(io.LimitReader(file, int64(maxAttachmentImageBytes)+1))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read uploaded file"})
			return
		}
	} else {
		// JSON body with base64 content.
		var req struct {
			Name string `json:"name"`
			MIME string `json:"mime"`
			Data string `json:"data"` // base64-encoded
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		name = req.Name
		mimeType = req.MIME
		decoded, err := base64.StdEncoding.DecodeString(req.Data)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid base64 data"})
			return
		}
		data = decoded
	}

	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "filename is required"})
		return
	}

	// Enforce size limits based on MIME type.
	if isImageMIME(mimeType) && len(data) > maxAttachmentImageBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": fmt.Sprintf("image too large: %d bytes, max %d bytes", len(data), maxAttachmentImageBytes)})
		return
	}
	if !isImageMIME(mimeType) && len(data) > maxAttachmentTextBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": fmt.Sprintf("file too large: %d bytes, max %d bytes", len(data), maxAttachmentTextBytes)})
		return
	}

	// Save to .hakase-tmp/attachments/ inside the workspace.
	tmpDir := filepath.Join(".", ".hakase-tmp", "attachments")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create temp directory"})
		return
	}

	// Verify the tmp dir is inside the sandbox.
	resolvedTmp, err := resolveFilePath(tmpDir)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "temp directory outside workspace"})
		return
	}

	ext := filepath.Ext(name)
	if ext == "" && mimeType != "" {
		// Infer extension from MIME.
		switch mimeType {
		case "image/png":
			ext = ".png"
		case "image/jpeg":
			ext = ".jpg"
		case "image/gif":
			ext = ".gif"
		case "image/webp":
			ext = ".webp"
		default:
			ext = ".bin"
		}
	}
	saveName := uuid.New().String() + ext
	savePath := filepath.Join(resolvedTmp, saveName)

	if err := os.WriteFile(savePath, data, 0o600); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save attachment"})
		return
	}

	// Build the relative path for the AttachmentRef.
	relPath, err := filepath.Rel(".", savePath)
	if err != nil {
		relPath = savePath
	}
	relPath = filepath.ToSlash(relPath)

	label := "@" + name
	if isImageMIME(mimeType) {
		label = name
	}

	writeJSON(w, http.StatusOK, attachmentUploadResponse{
		Name:  name,
		Path:  relPath,
		MIME:  mimeType,
		Label: label,
	})
}
