package web

import (
	"io"
	"log"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/channel/state"
	"amurru/hakase/internal/config"
	hctx "amurru/hakase/internal/context"
	"amurru/hakase/internal/session"
	"amurru/hakase/internal/web/handlers"
	"amurru/hakase/internal/web/middleware"
	"amurru/hakase/internal/web/sse"
	"google.golang.org/adk/v2/runner"
)

// RegisterRoutes sets up all routes on the given chi router.
// Unauthenticated endpoints (/api/health, /api/login) are registered first,
// then the auth middleware group wraps the remaining API routes.
// The SPA catch-all is mounted last.
func RegisterRoutes(r chiRouter, assets http.FileSystem, jwtKey []byte, sessionSvc *session.SessionService, bridge *sse.EventBridge, runner *runner.Runner, runtime *hakaseagent.Runtime, history *hctx.HistoryBuilder, approvalGate *handlers.WebApprovalGate, clarifyGate *handlers.WebClarifyGate, channelsStore *state.Store, channelsRunning func() bool, allowInsecureCookie bool) {
	// Middleware applied globally
	r.Use(CORSMiddleware())
	r.Use(RequestLogger())
	r.Use(middleware.SecurityHeaders())

	// Unauthenticated API routes
	r.Get("/api/health", handlers.HealthHandler())
	r.Post("/api/login", handlers.LoginHandler(jwtKey, credentialsPath(), 24*time.Hour, middleware.NewLoginRateLimiter(), allowInsecureCookie))

	// Authenticated API group
	r.Route("/api", func(r chiRouter) {
		r.Use(AuthMiddleware(jwtKey))
		r.Get("/me", handlers.MeHandler())
		r.Post("/logout", handlers.LogoutHandler())
		// Session API routes (task 20)
		if sessionSvc != nil {
			handlers.RegisterSessionRoutes(r, sessionSvc)
		}
		// Registered remote projects (project-registry DP-6..DP-10).
		handlers.RegisterProjectRoutes(r)
		// Chat (SSE streaming + message endpoint - task 21)
		if bridge != nil && runner != nil && runtime != nil && sessionSvc != nil {
			handlers.RegisterChatRoutes(r, bridge, sessionSvc, runner, runtime, history)
		}
		// Approval/Clarify response endpoints (task 22)
		if approvalGate != nil {
			handlers.RegisterApprovalRoutes(r, approvalGate)
		}
		if clarifyGate != nil {
			handlers.RegisterClarifyRoutes(r, clarifyGate)
		}
		// Task API routes (task 27)
		handlers.RegisterTaskRoutes(r)
		// Channel management (status, pairing code, revoke) - nil-tolerant.
		handlers.RegisterChannelsRoutes(r, channelsStore, channelsRunning)
		// File browsing routes (task 30)
		handlers.RegisterFileRoutes(r)
		// Knowledge management routes (task 31)
		var knowledgeDir string
		var skillDirs []string
		if cfg, err := config.LoadConfig("config.json"); err == nil {
			knowledgeDir = cfg.KnowledgeDir
			skillDirs = cfg.SkillDirs
		}
		handlers.RegisterKnowledgeRoutes(r, knowledgeDir)
		// Skill management routes (web UI skill manager)
		handlers.RegisterSkillRoutes(r, ".", skillDirs)
		// MCP server management routes (task 32)
		handlers.RegisterMCPRoutes(r)
		// Cron job management routes (task 33)
		handlers.RegisterCronRoutes(r)
		// Config file routes (task 34)
		handlers.RegisterConfigRoutes(r)
		// Media generation routes (MG-010)
		handlers.RegisterMediaRoutes(r)
	})

	// SPA handler: serves static assets with cache control, falls back to index.html.
	// Only mounted when assets are provided (nil = API-only mode, skip SPA).
	if assets != nil {
		r.Get("/*", spaHandler(assets))
	}
}

// spaHandler returns an http.HandlerFunc that serves the SPA.
// Static files (with extensions) are served with cache headers.
// Paths without extensions fall back to serving index.html (SPA routing).
func spaHandler(assets http.FileSystem) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")

		if path == "" {
			serveIndexHTML(w, assets)
			return
		}

		// If the path has a file extension and the file exists, serve it.
		if hasExtension(path) {
			f, err := assets.Open(path)
			if err == nil {
				f.Close()
				setCacheHeaders(w, path)
				serveFile(w, r, assets, path)
				return
			}
			// File not found: 404 for requests with extensions (broken asset links)
			http.NotFound(w, r)
			return
		}

		// No extension: SPA route. Fall back to index.html.
		serveIndexHTML(w, assets)
	}
}

// serveIndexHTML serves the index.html file with no-cache headers.
func serveIndexHTML(w http.ResponseWriter, assets http.FileSystem) {
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	serveFile(w, nil, assets, "index.html")
}

// serveFile serves a file from the assets filesystem.
func serveFile(w http.ResponseWriter, r *http.Request, assets http.FileSystem, name string) {
	f, err := assets.Open(name)
	if err != nil {
		if r == nil {
			log.Printf("web: failed to open %s: %v", name, err)
		}
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	// Set Content-Type from the file extension. Go's content sniffing
	// (http.DetectContentType, applied on first write when no header is set)
	// classifies JavaScript as text/plain, which browsers reject for ES
	// module scripts. All SPA asset types (js, css, svg, html) are in Go's
	// builtin MIME table, so this does not depend on the host system.
	if ctype := mime.TypeByExtension(filepath.Ext(name)); ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}

	// Get file info for size
	info, err := f.Stat()
	if err != nil {
		// Serve content without size if stat fails
		w.WriteHeader(http.StatusOK)
		io.Copy(w, f)
		return
	}

	// Serve with content length
	if info.Size() > 0 {
		buf := make([]byte, info.Size())
		n, err := io.ReadFull(f, buf)
		if err != nil && err != io.ErrUnexpectedEOF {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(buf[:n])
	} else {
		w.WriteHeader(http.StatusOK)
		io.Copy(w, f)
	}
}

// setCacheHeaders sets appropriate Cache-Control headers based on the asset path.
func setCacheHeaders(w http.ResponseWriter, path string) {
	if isHashedAsset(path) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
}

// hasExtension returns true if the path has a file extension.
func hasExtension(path string) bool {
	base := filepath.Base(path)
	return strings.Contains(base, ".")
}

// isHashedAsset returns true for assets that include a content hash in the filename.
func isHashedAsset(path string) bool {
	return strings.HasPrefix(path, "assets/")
}

// chiRouter is a minimal interface matching chi.Router's methods used in RegisterRoutes.
type chiRouter interface {
	Use(middlewares ...func(http.Handler) http.Handler)
	Get(pattern string, handlerFn http.HandlerFunc)
	Post(pattern string, handlerFn http.HandlerFunc)
	Put(pattern string, handlerFn http.HandlerFunc)
	Delete(pattern string, handlerFn http.HandlerFunc)
	Patch(pattern string, handlerFn http.HandlerFunc)
	Route(pattern string, fn func(r chiRouter))
}

// credentialsPath returns the path to the credentials.json file.
func credentialsPath() string {
	return filepath.Join(config.HakaseHome(), "credentials.json")
}
