package web

import (
	"context"
	"log"
	"net/http"
	"time"

	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/session"
	"amurru/hakase/internal/web/handlers"
	"amurru/hakase/internal/web/sse"
	"github.com/go-chi/chi/v5"
	"google.golang.org/adk/v2/runner"
)

// Server is the hakase web server. It serves the embedded SPA and API routes.
type Server struct {
	router     chi.Router
	jwtKey     []byte
	sessionSvc *session.SessionService
	server     *http.Server

	// Chat dependencies (optional - set before RegisterDefaults to enable chat routes).
	bridge  *sse.EventBridge
	runner  *runner.Runner
	runtime *hakaseagent.Runtime

	// Gate dependencies (optional - set before RegisterDefaults to enable approval/clarify routes).
	approvalGate *handlers.WebApprovalGate
	clarifyGate  *handlers.WebClarifyGate
}

// NewServer creates a new web Server with the given configuration.
// JWT signing key is required for auth middleware.
// sessionSvc is required for session API routes and may be nil for testing.
func NewServer(jwtKey []byte, sessionSvc *session.SessionService) *Server {
	s := &Server{
		jwtKey:     jwtKey,
		sessionSvc: sessionSvc,
	}
	s.router = chi.NewRouter()
	return s
}

// SetChatDeps configures the SSE bridge, agent runner, and runtime for
// chat endpoints. Must be called before RegisterDefaults.
func (s *Server) SetChatDeps(bridge *sse.EventBridge, runner *runner.Runner, runtime *hakaseagent.Runtime) {
	s.bridge = bridge
	s.runner = runner
	s.runtime = runtime
}

// SetGates configures the web-based approval and clarify gates for interactive
// endpoints. Must be called before RegisterDefaults. The gates implement the
// interfaces.ApprovalGate and interfaces.ClarifyGate contracts, allowing the
// agent to block on user responses via HTTP.
func (s *Server) SetGates(approvalGate *handlers.WebApprovalGate, clarifyGate *handlers.WebClarifyGate) {
	s.approvalGate = approvalGate
	s.clarifyGate = clarifyGate
}

// Router returns the underlying chi.Router for testing or further customization.
func (s *Server) Router() chi.Router {
	return s.router
}

// RegisterDefaults configures the router with default middleware, API routes,
// and SPA handler. assets is the filesystem providing the frontend assets.
func (s *Server) RegisterDefaults(assets http.FileSystem) {
	RegisterRoutes(&chiRouterAdapter{s.router}, assets, s.jwtKey, s.sessionSvc, s.bridge, s.runner, s.runtime, s.approvalGate, s.clarifyGate)
}

// Run starts the HTTP server on the given address and blocks until
// the server is shut down. addr should be in "host:port" format.
func (s *Server) Run(addr string) error {
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // SSE connections are long-lived
		IdleTimeout:  120 * time.Second,
	}
	s.server = srv
	log.Printf("web: starting server on %s", addr)
	return srv.ListenAndServe()
}

// Shutdown gracefully stops the server with a timeout.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

// chiRouterAdapter adapts chi.Router to the chiRouter interface used in spa.go.
type chiRouterAdapter struct {
	r chi.Router
}

func (a *chiRouterAdapter) Use(middlewares ...func(http.Handler) http.Handler) {
	for _, m := range middlewares {
		a.r.Use(m)
	}
}

func (a *chiRouterAdapter) Get(pattern string, handlerFn http.HandlerFunc) {
	a.r.Get(pattern, handlerFn)
}

func (a *chiRouterAdapter) Post(pattern string, handlerFn http.HandlerFunc) {
	a.r.Post(pattern, handlerFn)
}

func (a *chiRouterAdapter) Delete(pattern string, handlerFn http.HandlerFunc) {
	a.r.Delete(pattern, handlerFn)
}

func (a *chiRouterAdapter) Patch(pattern string, handlerFn http.HandlerFunc) {
	a.r.Patch(pattern, handlerFn)
}

func (a *chiRouterAdapter) Route(pattern string, fn func(r chiRouter)) {
	a.r.Route(pattern, func(r chi.Router) {
		fn(&chiRouterAdapter{r})
	})
}
