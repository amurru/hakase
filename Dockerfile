# syntax=docker/dockerfile:1
# Zero-toolchain "try it" path (docs/ROADMAP.md Tier 4 item 2).
#
# Build:  docker build -t hakase .
# Run:    docker run -d -p 8080:8080 -e HAKASE_API_KEY=... \
#           -e HAKASE_ADMIN_USERNAME=admin -e HAKASE_ADMIN_PASSWORD=... \
#           -v hakase-data:/data hakase
#
# With no config file, configuration comes entirely from HAKASE_* environment
# variables (internal/config envConfigSet), and the admin login is
# provisioned on first boot from HAKASE_ADMIN_USERNAME/HAKASE_ADMIN_PASSWORD
# (internal/cli.ProvisionAdminFromEnv). /data holds all runtime state
# (HAKASE_HOME) and is the only volume you need.

# --- webui: vite + vue build (feeds go:embed) -------------------------------
FROM node:22-alpine AS webui
WORKDIR /src/webui
RUN npm install -g pnpm@11
COPY webui/package.json webui/pnpm-lock.yaml ./
RUN --mount=type=cache,id=pnpm,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile
COPY webui/ ./
RUN pnpm build

# --- build: static binary with the SPA embedded -----------------------------
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,id=gomod,target=/go/pkg/mod \
    go mod download
COPY . .
COPY --from=webui /src/webui/dist internal/web/dist
RUN --mount=type=cache,id=gomod,target=/go/pkg/mod \
    --mount=type=cache,id=gobuild,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -tags prod -o /out/hakase ./cmd/hakase/

# --- runtime: python3 is the only hard-required tool (skill venv) -----------
FROM python:3.12-slim-bookworm
RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates \
    && rm -rf /var/lib/apt/lists/*
RUN useradd -m -u 1000 hakase \
    && mkdir -p /data \
    && chown hakase:hakase /data
COPY --from=build /out/hakase /usr/local/bin/hakase

# Runtime state (config.json, credentials, knowledge, outputs/) all lives
# under HAKASE_HOME; WORKDIR keeps relative writes (outputs/) inside it too.
ENV HAKASE_HOME=/data
WORKDIR /data
VOLUME /data
USER hakase
EXPOSE 8080
ENTRYPOINT ["hakase", "web", "--host", "0.0.0.0"]
