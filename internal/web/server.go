package web

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// Server is the hakase web server. It serves the embedded SPA and API routes.
type Server struct {
	router  chi.Router
	jwtKey  []byte
	server  *http.Server
}

// NewServer creates a new web Server with the given configuration.
// JWT signing key is required for auth middleware.
func NewServer(jwtKey []byte) *Server {
	s := &Server{
		jwtKey: jwtKey,
	}
	s.router = chi.NewRouter()
	return s
}

// Router returns the underlying chi.Router for testing or further customization.
func (s *Server) Router() chi.Router {
	return s.router
}

// RegisterDefaults configures the router with default middleware, API routes,
// and SPA handler. assets is the filesystem providing the frontend assets.
func (s *Server) RegisterDefaults(assets http.FileSystem) {
	RegisterRoutes(&chiRouterAdapter{s.router}, assets, s.jwtKey)
}

// Run starts the HTTP server on the given address and blocks until
// the server is shut down. addr should be in "host:port" format.
func (s *Server) Run(addr string) error {
	srv := &http.Server{
		Addr:         addr,
		Handler:      s.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
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

func (a *chiRouterAdapter) Route(pattern string, fn func(r chiRouter)) {
	a.r.Route(pattern, func(r chi.Router) {
		fn(&chiRouterAdapter{r})
	})
}
