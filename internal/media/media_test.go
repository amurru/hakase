package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"amurru/hakase/internal/config"
	"amurru/hakase/internal/sandbox"
)

// helper to create temp store with no sandbox
func tempStore(t *testing.T) *Store {
	t.Helper()
	sandbox.CurrentSandbox = nil
	dir := t.TempDir() + "/outputs/media"
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestStoreAllocateAndWrite(t *testing.T) {
	s := tempStore(t)
	path, err := s.Allocate(".png")
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if filepath.Ext(path) != ".png" {
		t.Fatalf("ext = %q, want .png", filepath.Ext(path))
	}
	// traversal via ext not allowed
	if _, err := s.Allocate("../evil.png"); err == nil {
		t.Fatal("expected error for traversal ext")
	}
	if _, err := s.Allocate(".exe"); err == nil {
		t.Fatal("expected error for disallowed ext")
	}
	// atomic write and size cap
	data := []byte("hello")
	if err := s.Write(path, bytes.NewReader(data), 20<<20); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "hello" {
		t.Fatalf("write content mismatch")
	}
	// size cap
	bigPath, _ := s.Allocate(".png")
	large := make([]byte, 30)
	if err := s.Write(bigPath, bytes.NewReader(large), 20); err == nil {
		t.Fatal("expected size cap error")
	}
}

func TestProviderValidation(t *testing.T) {
	if err := (ImageRequest{Prompt: ""}).Validate(); err == nil {
		t.Fatal("empty prompt should fail")
	}
	if err := (ImageRequest{Prompt: "hi", Width: 100}).Validate(); err == nil {
		t.Fatal("small width should fail")
	}
	if err := (ImageRequest{Prompt: "hi", Height: 5000}).Validate(); err == nil {
		t.Fatal("large height should fail")
	}
	// valid
	if err := (ImageRequest{Prompt: "hi", Width: 512, Height: 512}).Validate(); err != nil {
		t.Fatalf("valid request failed: %v", err)
	}
	// clamp
	r := ImageRequest{Width: 100, Height: 5000}
	w, h := r.ClampedSize()
	if w != 256 || h != 2048 {
		t.Fatalf("clamp got %d %d", w, h)
	}
}

func TestRegistryResolve(t *testing.T) {
	sandbox.CurrentSandbox = nil
	s := tempStore(t)
	cfg := config.MediaConfig{}
	cfg.ApplyDefaults()
	// zero-config: pil is only healthy
	reg, err := NewRegistry(cfg, nil, s)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	p, err := reg.Resolve("image")
	if err != nil {
		t.Fatalf("Resolve image: %v", err)
	}
	if p.Name() != "pil" {
		t.Fatalf("expected pil, got %s", p.Name())
	}
	// explicit missing
	if _, err := reg.ResolveForProvider("image", "unknown"); err == nil {
		t.Fatal("expected error for unknown provider")
	}
	// pil does not support video
	if _, err := reg.ResolveForProvider("video", "pil"); err == nil {
		t.Fatal("pil should not support video")
	}
	// auto video with no provider should error verbatim
	if _, err := reg.Resolve("video"); err == nil || !contains(err.Error(), "video generation requires a provider") {
		t.Fatalf("expected video requires provider error, got %v", err)
	}
	// with fal key, auto video should resolve to fal
	cfg2 := config.MediaConfig{}
	cfg2.ApplyDefaults()
	cfg2.FalKey = "test-key"
	reg2, _ := NewRegistry(cfg2, nil, s)
	p2, err := reg2.Resolve("video")
	if err != nil {
		t.Fatalf("Resolve video with fal: %v", err)
	}
	if p2.Name() != "fal" {
		t.Fatalf("expected fal, got %s", p2.Name())
	}
	// with openai key, auto image should prefer openai over pil
	cfg3 := config.MediaConfig{}
	cfg3.ApplyDefaults()
	cfg3.OpenAIImageKey = "sk-test"
	reg3, _ := NewRegistry(cfg3, nil, s)
	p3, _ := reg3.Resolve("image")
	if p3.Name() != "openai" {
		t.Fatalf("expected openai, got %s", p3.Name())
	}
	// unhealthy skip: fal key missing, order includes fal but should skip to pil
	cfg4 := config.MediaConfig{}
	cfg4.ApplyDefaults()
	// no keys -> pil
	reg4, _ := NewRegistry(cfg4, nil, s)
	p4, _ := reg4.Resolve("image")
	if p4.Name() != "pil" {
		t.Fatalf("expected pil skip, got %s", p4.Name())
	}
}

func TestRegistrySemaphore(t *testing.T) {
	s := tempStore(t)
	cfg := config.MediaConfig{}
	cfg.ApplyDefaults()
	cfg.MaxConcurrent = 1
	reg, _ := NewRegistry(cfg, nil, s)
	ctx := context.Background()
	if err := reg.Acquire(ctx, "pil"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// second acquire should block until release, test timeout
	ctx2, cancel := context.WithCancel(ctx)
	cancel() // already canceled
	if err := reg.Acquire(ctx2, "pil"); err == nil {
		t.Fatal("expected error on canceled context")
	}
	reg.Release("pil")
	// should succeed now
	if err := reg.Acquire(ctx, "pil"); err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	reg.Release("pil")
}

func TestPilGenerate(t *testing.T) {
	s := tempStore(t)
	p := NewPilProvider(s)
	// empty prompt error
	if _, err := p.GenerateImage(context.Background(), ImageRequest{Prompt: ""}); err == nil {
		t.Fatal("expected prompt required")
	}
	seed := int64(123)
	req := ImageRequest{Prompt: "a poster for baoyu infographic about Tokyo transit", Width: 512, Height: 512, Seed: &seed}
	res, err := p.GenerateImage(context.Background(), req)
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	if res.Provider != "pil" || res.Width != 512 || res.Height != 512 {
		t.Fatalf("unexpected result: %+v", res)
	}
	// valid PNG
	f, _ := os.Open(res.Path)
	defer f.Close()
	if _, err := png.Decode(f); err != nil {
		t.Fatalf("png decode: %v", err)
	}
	// determinism
	res2, _ := p.GenerateImage(context.Background(), req)
	b1, _ := os.ReadFile(res.Path)
	b2, _ := os.ReadFile(res2.Path)
	if !bytes.Equal(b1, b2) {
		t.Fatal("pil not deterministic")
	}
}

// TestProviderResultPathsAreWorkspaceRelative locks the agent-facing result
// contract: MediaResult.Path and the markdown snippet must use a
// workspace-relative path (outputs/media/<ulid>.<ext>) so the web UI
// mediaLinks plugin rewrites it to /api/files/inline. Regression guard:
// absolute paths leaked into chat markdown and the browser resolved them
// against the page origin (http://host/home/... -> 404).
func TestProviderResultPathsAreWorkspaceRelative(t *testing.T) {
	t.Chdir(t.TempDir()) // workspace root; the store resolves under cwd
	sandbox.CurrentSandbox = nil
	s, err := NewStore("outputs/media")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	p := NewPilProvider(s)
	res, err := p.GenerateImage(context.Background(), ImageRequest{Prompt: "a horse playing chess"})
	if err != nil {
		t.Fatalf("GenerateImage: %v", err)
	}
	const wantPrefix = "outputs/media/"
	if filepath.IsAbs(res.Path) || strings.HasPrefix(res.Path, "..") || !strings.HasPrefix(res.Path, wantPrefix) {
		t.Fatalf("Path = %q, want workspace-relative %s<ulid>.png", res.Path, wantPrefix)
	}
	if !strings.HasSuffix(res.Markdown, "]("+res.Path+")") {
		t.Fatalf("Markdown = %q, want it to embed the relative path %q", res.Markdown, res.Path)
	}
	// The reported path must resolve to the real file from the workspace
	// root (cwd) - exactly how sandbox.ResolveScopedPath serves it via
	// GET /api/files/inline?path=...
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatalf("file missing at workspace-relative path: %v", err)
	}
}

func TestOpenAIClampAndAuth(t *testing.T) {
	// per-model clamping
	if w, h := clampSizeForModel("gpt-image-1-mini", 512, 512); w != 1024 || h != 1024 {
		t.Fatalf("gpt-image-1 clamp got %d %d", w, h)
	}
	if w, h := clampSizeForModel("dall-e-3", 2000, 1000); w != 1792 || h != 1024 {
		t.Fatalf("dall-e-3 clamp got %d %d", w, h)
	}
	// unknown passthrough
	if w, h := clampSizeForModel("custom/slug", 2048, 2048); w != 2048 || h != 2048 {
		t.Fatalf("custom passthrough got %d %d", w, h)
	}
	// gpt-image-2 divisible by 16
	w, h := clampSizeForModel("gpt-image-2", 1000, 1000)
	if w%16 != 0 || h%16 != 0 {
		t.Fatalf("gpt-image-2 not divisible by 16: %d %d", w, h)
	}
}

func TestOpenAIProviderMock(t *testing.T) {
	// b64 success, 401, 404 hint, response_format gating, path override
	// helper to create 1x1 png b64
	imgData := base64.StdEncoding.EncodeToString(mustPNGBytes())

	// success
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/images/generations" {
			// check response_format absent for gpt-image-1-mini
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, ok := body["response_format"]; ok {
				t.Errorf("response_format should not be sent for gpt-image-1-mini")
			}
			if body["model"] != "gpt-image-1-mini" {
				t.Errorf("model = %v", body["model"])
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"created":1,"data":[{"b64_json":"%s"}]}`, imgData)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	s := tempStore(t)
	cfg := config.MediaConfig{}
	cfg.ApplyDefaults()
	cfg.OpenAIImageKey = "sk-test"
	cfg.OpenAIImageBaseURL = srv.URL
	cfg.OpenAIImagePath = "/images/generations"
	reg, _ := NewRegistry(cfg, nil, s)
	p, _ := reg.Get("openai")
	res, err := p.GenerateImage(context.Background(), ImageRequest{Prompt: "hello"})
	if err != nil {
		t.Fatalf("GenerateImage success: %v", err)
	}
	if res.MimeType != "image/png" {
		t.Fatalf("mime = %s", res.MimeType)
	}

	// 401
	srv401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv401.Close()
	cfg401 := config.MediaConfig{}
	cfg401.ApplyDefaults()
	cfg401.OpenAIImageKey = "bad"
	cfg401.OpenAIImageBaseURL = srv401.URL
	p401 := NewOpenAIProvider(cfg401, nil, s)
	if _, err := p401.GenerateImage(context.Background(), ImageRequest{Prompt: "hi"}); err == nil || !contains(err.Error(), "401") {
		t.Fatalf("expected 401, got %v", err)
	}

	// 404 hint
	srv404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv404.Close()
	cfg404 := config.MediaConfig{}
	cfg404.ApplyDefaults()
	cfg404.OpenAIImageKey = "sk"
	cfg404.OpenAIImageBaseURL = srv404.URL
	p404 := NewOpenAIProvider(cfg404, nil, s)
	if _, err := p404.GenerateImage(context.Background(), ImageRequest{Prompt: "hi"}); err == nil || !contains(err.Error(), "openai_image_path") {
		t.Fatalf("expected 404 hint, got %v", err)
	}

	// dall-e-3 should send response_format
	srvDalle := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["response_format"] != "b64_json" {
			t.Errorf("dall-e-3 should send response_format")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[{"b64_json":"%s"}]}`, imgData)
	}))
	defer srvDalle.Close()
	cfgDalle := config.MediaConfig{}
	cfgDalle.ApplyDefaults()
	cfgDalle.OpenAIImageKey = "sk"
	cfgDalle.OpenAIImageBaseURL = srvDalle.URL
	cfgDalle.OpenAIImageModel = "dall-e-3"
	pDalle := NewOpenAIProvider(cfgDalle, nil, s)
	if _, err := pDalle.GenerateImage(context.Background(), ImageRequest{Prompt: "hi", Model: "dall-e-3"}); err != nil {
		t.Fatalf("dall-e-3 mock: %v", err)
	}

	// path override for OpenRouter
	srvOR := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images" {
			t.Errorf("expected /images, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[{"b64_json":"%s"}]}`, imgData)
	}))
	defer srvOR.Close()
	cfgOR := config.MediaConfig{}
	cfgOR.ApplyDefaults()
	cfgOR.OpenAIImageKey = "sk-or"
	cfgOR.OpenAIImageBaseURL = srvOR.URL
	cfgOR.OpenAIImagePath = "/images"
	pOR := NewOpenAIProvider(cfgOR, nil, s)
	if _, err := pOR.GenerateImage(context.Background(), ImageRequest{Prompt: "hi"}); err != nil {
		t.Fatalf("openrouter path: %v", err)
	}
}

func mustPNGBytes() []byte {
	// 1x1 transparent png
	b, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+ip1sAAAAASUVORK5CYII=")
	return b
}

func TestFalProviderMock(t *testing.T) {
	s := tempStore(t)
	// mock fal queue + poll + download success - need to bypass SSRF for 127.0.0.1
	origCheck := checkHostPublic
	checkHostPublic = func(host string) error { return nil }
	defer func() { checkHostPublic = origCheck }()

	imgData := mustPNGBytes()
	// download server
	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(imgData)
	}))
	defer dlSrv.Close()

	pollCount := 0
	falSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"request_id":"req_123"}`)
			return
		}
		if r.Method == "GET" && contains(r.URL.Path, "/status") {
			pollCount++
			if pollCount < 2 {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"status":"IN_PROGRESS"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"status":"COMPLETED","response":{"images":[{"url":"%s/image.png"}]}}`, dlSrv.URL)
			return
		}
		http.NotFound(w, r)
	}))
	defer falSrv.Close()

	cfg := config.MediaConfig{}
	cfg.ApplyDefaults()
	cfg.FalKey = "test-key"
	cfg.FalBaseURL = falSrv.URL
	p := NewFalProvider(cfg, nil, s)
	res, err := p.GenerateImage(context.Background(), ImageRequest{Prompt: "a cat", Width: 512, Height: 512})
	if err != nil {
		t.Fatalf("fal generate: %v", err)
	}
	if res.Provider != "fal" {
		t.Fatalf("provider = %s", res.Provider)
	}
	// restore for next sub-tests (401 still needs bypass, but SSRF needs real check)
	checkHostPublic = func(host string) error { return nil }

	// 401
	fal401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer fal401.Close()
	cfg401 := config.MediaConfig{}
	cfg401.ApplyDefaults()
	cfg401.FalKey = "bad"
	cfg401.FalBaseURL = fal401.URL
	p401 := NewFalProvider(cfg401, nil, s)
	if _, err := p401.GenerateImage(context.Background(), ImageRequest{Prompt: "hi"}); err == nil || !contains(err.Error(), "401") {
		t.Fatalf("expected 401, got %v", err)
	}

	// SSRF rejection: result URL private IP (restore real check)
	checkHostPublic = origCheck
	falSSRF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			fmt.Fprint(w, `{"request_id":"req_ssrf"}`)
			return
		}
		fmt.Fprint(w, `{"status":"COMPLETED","response":{"images":[{"url":"http://127.0.0.1/evil.png"}]}}`)
	}))
	defer falSSRF.Close()
	cfgSSRF := config.MediaConfig{}
	cfgSSRF.ApplyDefaults()
	cfgSSRF.FalKey = "k"
	cfgSSRF.FalBaseURL = falSSRF.URL
	pSSRF := NewFalProvider(cfgSSRF, nil, s)
	if _, err := pSSRF.GenerateImage(context.Background(), ImageRequest{Prompt: "hi"}); err == nil || !contains(err.Error(), "blocked") {
		t.Fatalf("expected SSRF blocked, got %v", err)
	}
	// re-allow for remaining tests
	checkHostPublic = func(host string) error { return nil }

	// wan mapping: aspect ratio nearest
	if aspectRatioFromWH(1920, 1080) != "16:9" {
		t.Fatalf("1920x1080 should be 16:9")
	}
	if aspectRatioFromWH(1080, 1920) != "9:16" {
		t.Fatalf("1080x1920 should be 9:16")
	}
	// poll timeout
	falTimeout := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			fmt.Fprint(w, `{"request_id":"req_timeout"}`)
			return
		}
		fmt.Fprint(w, `{"status":"IN_PROGRESS"}`)
	}))
	defer falTimeout.Close()
	cfgTimeout := config.MediaConfig{}
	cfgTimeout.ApplyDefaults()
	cfgTimeout.FalKey = "k"
	cfgTimeout.FalBaseURL = falTimeout.URL
	pTimeout := NewFalProvider(cfgTimeout, nil, s)
	// A cancelled context must abort generation immediately.
	ctx3, cancel3 := context.WithCancel(context.Background())
	cancel3()
	if _, err := pTimeout.GenerateImage(ctx3, ImageRequest{Prompt: "hi"}); err == nil {
		t.Fatal("expected error on canceled context")
	}
}

func TestOpenAIVideoMock(t *testing.T) {
	s := tempStore(t)
	origCheck := checkHostPublic
	checkHostPublic = func(host string) error { return nil }
	defer func() { checkHostPublic = origCheck }()

	mp4 := []byte("fake-mp4-bytes")
	dlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Write(mp4)
	}))
	defer dlSrv.Close()

	polls := 0
	var (
		gotBodyMu sync.Mutex
		gotBody   map[string]interface{}
	)
	// storeGotBody / submitBody serialize access to gotBody between the
	// httptest handler goroutine and this test goroutine so `go test -race`
	// stays stable.
	storeGotBody := func(m map[string]interface{}) {
		gotBodyMu.Lock()
		defer gotBodyMu.Unlock()
		gotBody = m
	}
	submitBody := func(key string) interface{} {
		gotBodyMu.Lock()
		defer gotBodyMu.Unlock()
		return gotBody[key]
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/videos":
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode submit body: %v", err)
			}
			storeGotBody(body)
			if body["model"] != "google/veo-3.1-lite" {
				t.Errorf("model = %v", body["model"])
			}
			if body["generate_audio"] != false {
				t.Errorf("generate_audio = %v, want explicit false (cheapest tier)", body["generate_audio"])
			}
			if body["resolution"] != "720p" {
				t.Errorf("resolution = %v", body["resolution"])
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(202)
			fmt.Fprint(w, `{"id":"job_1","polling_url":"/videos/job_1","status":"pending"}`)
		case r.Method == "GET" && r.URL.Path == "/videos/job_1":
			polls++
			w.Header().Set("Content-Type", "application/json")
			if polls < 2 {
				fmt.Fprint(w, `{"status":"in_progress"}`)
				return
			}
			fmt.Fprintf(w, `{"status":"completed","unsigned_urls":["%s/clip.mp4"]}`, dlSrv.URL)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.MediaConfig{}
	cfg.ApplyDefaults()
	cfg.OpenAIImageKey = "sk-or-test"
	cfg.OpenAIImageBaseURL = srv.URL
	p := NewOpenAIProvider(cfg, nil, s)

	// text-to-video happy path
	res, err := p.GenerateVideo(context.Background(), VideoRequest{Prompt: "a horse playing chess", DurationSeconds: 5})
	if err != nil {
		t.Fatalf("GenerateVideo: %v", err)
	}
	if res.Provider != "openai" || res.MimeType != "video/mp4" || res.Model != "google/veo-3.1-lite" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if b, _ := os.ReadFile(res.Path); !bytes.Equal(b, mp4) {
		t.Fatal("stored video bytes mismatch")
	}

	// image-to-video: local file inlined as first-frame data URL
	imgPath := filepath.Join(t.TempDir(), "frame.png")
	if err := os.WriteFile(imgPath, mustPNGBytes(), 0644); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	storeGotBody(nil)
	if _, err := p.GenerateVideo(context.Background(), VideoRequest{Prompt: "animate it", ImageRef: imgPath}); err != nil {
		t.Fatalf("i2v GenerateVideo: %v", err)
	}
	frames, ok := submitBody("frame_images").([]interface{})
	if !ok || len(frames) != 1 {
		t.Fatalf("frame_images missing: %v", submitBody("frame_images"))
	}
	frame, _ := frames[0].(map[string]interface{})
	if frame["frame_type"] != "first_frame" || frame["type"] != "image_url" {
		t.Fatalf("frame shape wrong: %v", frame)
	}
	iu, _ := frame["image_url"].(map[string]interface{})
	urlStr, _ := iu["url"].(string)
	if !strings.HasPrefix(urlStr, "data:image/png;base64,") {
		t.Fatalf("local image not inlined as data url: %.60s", urlStr)
	}

	// http(s) image refs pass through untouched
	storeGotBody(nil)
	if _, err := p.GenerateVideo(context.Background(), VideoRequest{Prompt: "x", ImageRef: "https://example.com/a.png"}); err != nil {
		t.Fatalf("url i2v: %v", err)
	}
	frames = submitBody("frame_images").([]interface{})
	frame = frames[0].(map[string]interface{})
	iu = frame["image_url"].(map[string]interface{})
	if iu["url"] != "https://example.com/a.png" {
		t.Fatalf("https ref altered: %v", iu["url"])
	}

	// failed job surfaces API error text
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			fmt.Fprint(w, `{"id":"job_f","polling_url":"/videos/job_f","status":"pending"}`)
			return
		}
		fmt.Fprint(w, `{"status":"failed","error":"duration 5 is not supported; use one of 4,6,8"}`)
	}))
	defer failSrv.Close()
	cfgFail := cfg
	cfgFail.OpenAIImageBaseURL = failSrv.URL
	pFail := NewOpenAIProvider(cfgFail, nil, s)
	if _, err := pFail.GenerateVideo(context.Background(), VideoRequest{Prompt: "x"}); err == nil || !contains(err.Error(), "duration 5 is not supported") {
		t.Fatalf("expected surfaced failure message, got %v", err)
	}

	// 401
	unauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer unauth.Close()
	cfg401 := cfg
	cfg401.OpenAIImageBaseURL = unauth.URL
	p401 := NewOpenAIProvider(cfg401, nil, s)
	if _, err := p401.GenerateVideo(context.Background(), VideoRequest{Prompt: "x"}); err == nil || !contains(err.Error(), "401") {
		t.Fatalf("expected 401, got %v", err)
	}

	// no key anywhere
	noKey := config.MediaConfig{}
	noKey.ApplyDefaults()
	noKey.OpenAIImageKey = ""
	pNoKey := NewOpenAIProvider(noKey, nil, s)
	if _, err := pNoKey.GenerateVideo(context.Background(), VideoRequest{Prompt: "x"}); err == nil || !contains(err.Error(), "auth") {
		t.Fatalf("expected auth error without key, got %v", err)
	}
}

func TestRegistryOpenAIVideoResolution(t *testing.T) {
	sandbox.CurrentSandbox = nil
	s := tempStore(t)
	cfg := config.MediaConfig{}
	cfg.ApplyDefaults()
	cfg.OpenAIImageKey = "sk-test"
	reg, err := NewRegistry(cfg, nil, s)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	p, err := reg.Resolve("video")
	if err != nil {
		t.Fatalf("Resolve video with openai key: %v", err)
	}
	if p.Name() != "openai" {
		t.Fatalf("expected openai for video, got %s", p.Name())
	}
	// explicit provider hint also works now
	p2, err := reg.ResolveForProvider("video", "openai")
	if err != nil || p2.Name() != "openai" {
		t.Fatalf("explicit openai video resolve failed: %v", err)
	}
}

func TestRegistryVideoOnlyKeyResolution(t *testing.T) {
	s := tempStore(t)
	cfg := config.MediaConfig{}
	cfg.ApplyDefaults()
	cfg.OpenAIVideoKey = "sk-video-only"
	reg, err := NewRegistry(cfg, nil, s)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	p, err := reg.Resolve("video")
	if err != nil {
		t.Fatalf("Resolve video with video-only key: %v", err)
	}
	if p.Name() != "openai" {
		t.Fatalf("expected openai for video with only openai_video_key, got %s", p.Name())
	}
	// Image resolution must not select openai without an image key.
	if ip, ierr := reg.Resolve("image"); ierr == nil && ip.Name() == "openai" {
		t.Fatal("openai resolved for image without openai_image_key")
	}
}

func TestFalVideoRejectsImageInput(t *testing.T) {
	s := tempStore(t)
	cfg := config.MediaConfig{}
	cfg.ApplyDefaults()
	cfg.FalKey = "k"
	p := NewFalProvider(cfg, nil, s)
	_, err := p.GenerateVideo(context.Background(), VideoRequest{Prompt: "x", ImageRef: "outputs/media/a.png"})
	if err == nil || !contains(err.Error(), "image-to-video") {
		t.Fatalf("expected actionable fal i2v error, got %v", err)
	}
}

func TestToolsErrorStrings(t *testing.T) {
	s := tempStore(t)
	cfg := config.MediaConfig{}
	cfg.ApplyDefaults()
	reg, err := NewRegistry(cfg, nil, s)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	tools, err := CreateMediaTools(reg, nil)
	if err != nil {
		t.Fatalf("CreateMediaTools: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}
	// No keys configured: video auto-resolution must fail with the verbatim
	// actionable message shared by the registry and the generate_video tool.
	if _, err := reg.Resolve("video"); err == nil || err.Error() != videoNoProviderMsg {
		t.Fatalf("video error string mismatch: %v", err)
	}
	// Audio is a v1 stub with a fixed not-wired message.
	const wantAudio = "audio generation is not wired in this build: openai TTS is planned for v2"
	if _, err := reg.Resolve("audio"); err == nil || err.Error() != wantAudio {
		t.Fatalf("audio error string mismatch: got %v, want %q", err, wantAudio)
	}
}

func TestManifestConcurrent(t *testing.T) {
	s := tempStore(t)
	// concurrent manifest appends
	var wg sync.WaitGroup
	n := 10
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			appendManifest(s, manifestEntry{
				TS:       "2026-08-21T02:00:00Z",
				Tool:     "generate_image",
				Prompt:   fmt.Sprintf("prompt %d", idx),
				Provider: "pil",
				Path:     fmt.Sprintf("outputs/media/%d.png", idx),
				Width:    1024,
				Height:   1024,
			})
		}(i)
	}
	wg.Wait()
	manifestPath := filepath.Join(s.Root(), "manifest.jsonl")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != n {
		t.Fatalf("expected %d lines, got %d", n, len(lines))
	}
	for _, l := range lines {
		var m map[string]interface{}
		if err := json.Unmarshal(l, &m); err != nil {
			t.Fatalf("invalid json line: %s", l)
		}
	}
}

func contains(s, sub string) bool {
	return bytes.Contains([]byte(s), []byte(sub))
}
