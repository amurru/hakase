# Hakase build pipeline
#
# Two build modes are supported via Go build tags (see internal/web/embed_*.go):
#
#   prod (default, !dev): the frontend assets are embedded into the binary
#       via //go:embed all:dist. The embedded files live in internal/web/dist,
#       which the frontend build mirrors from webui/dist (go:embed cannot
#       follow symlinks, so the mirror is a real directory copy).
#
#   dev:  the frontend is served live from ./webui/dist on disk (os.DirFS),
#       so frontend changes are visible without rebuilding the binary. Use
#       this with the Vite dev server for HMR-backed development.
#
# Targets:
#   build-frontend  Install webui deps, build the SPA, mirror it into
#                   internal/web/dist for embedding.
#   build           Full production build: frontend + Go binary (prod tag).
#   dev             Development mode. Runs two processes (see dev-frontend /
#                   dev-backend); this target prints the instructions.
#   dev-frontend    Start the Vite dev server (HMR) for the webui.
#   dev-backend     Build + run the Go web server with the dev tag (serves
#                   webui/dist from disk, API on :8080).
#   test            Run all Go tests.
#   clean           Remove frontend build output and the hakase binary.
#
# The `web` subcommand (./hakase web) is wired in a later task; until then
# dev-backend falls back to `go run` so the build tags are still exercised.

.PHONY: build-frontend build dev dev-frontend dev-backend test clean

# ---------------------------------------------------------------------------
# Frontend
# ---------------------------------------------------------------------------

# Install deps and produce webui/dist, then mirror it into internal/web/dist
# where embed_prod.go picks it up (//go:embed all:dist, relative to the
# package directory). A fresh mirror is mandatory before any prod build.
build-frontend:
	cd webui && pnpm install && pnpm build
	rm -rf internal/web/dist
	mkdir -p internal/web/dist
	cp -r webui/dist/. internal/web/dist/

# ---------------------------------------------------------------------------
# Production build
# ---------------------------------------------------------------------------

# Full production binary: embedded SPA + Go server. No internet needed beyond
# pnpm install (Go deps come from the module cache).
build: build-frontend
	go build -tags prod -o hakase ./cmd/hakase/

# ---------------------------------------------------------------------------
# Development
# ---------------------------------------------------------------------------

# Development runs two long-lived processes in separate terminals:
#   Terminal 1: make dev-frontend   (Vite dev server, HMR, port 5173)
#   Terminal 2: make dev-backend    (Go server, dev tag, port 8080)
# Vite proxies /api to the Go server (see webui/vite.config.ts), so open
# http://localhost:5173 in the browser.
dev:
	@echo "Development mode runs two processes in separate terminals:"
	@echo "  Terminal 1: make dev-frontend"
	@echo "  Terminal 2: make dev-backend"
	@echo ""
	@echo "Then open http://localhost:5173 (Vite proxies /api -> :8080)."
	@echo "The backend serves webui/dist from disk (dev tag, no embedded assets)."

# Vite dev server with hot module replacement.
dev-frontend:
	cd webui && pnpm install && pnpm dev

# Go web server with the dev build tag: serves webui/dist live from disk.
# `web` subcommand lands in a later task; `go run` keeps the tags exercised.
dev-backend:
	go run -tags dev ./cmd/hakase/ web

# ---------------------------------------------------------------------------
# Quality
# ---------------------------------------------------------------------------

test:
	go test ./...

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------

clean:
	rm -rf webui/dist internal/web/dist hakase
