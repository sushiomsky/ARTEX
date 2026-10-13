#!/usr/bin/env bash
# ARTEX update script: (1) Docker update (pull new image, rebuild)  (2) local compile update (rebuild binary)
# Mirrors install.sh: install handles first-time setup, update upgrades to a new version.
# No manual DB migration needed — artex idempotently replays schema.sql on every startup (including ADD COLUMN / CREATE
# INDEX IF NOT EXISTS), so "a restart is the migration". Data (pgdata volume, ./data, ./skills) is untouched.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }

# ── Optional: sync the repo to the latest code (compose / scripts / local-build sources all update through this) ───────
sync_repo(){
  [ -d .git ] && command -v git >/dev/null 2>&1 || { warn "not a git working copy, skipping git pull"; return; }
  [ "$(ask 'Pull the latest code (git pull --ff-only)? (y/n)' y)" = y ] || return
  if ! git pull --ff-only; then
    warn "git pull could not fast-forward (local changes or diverged branch) — resolve manually and retry; keeping the current code this time"
  fi
}

# ── 1. Docker update ───────────────────────────────
update_docker(){
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 \
    || die "docker / docker compose not found; deploy first with ./install.sh"
  [ -f .env ] || die ".env not found; run ./install.sh to complete first-time setup"

  # Optional: upgrade to a specific version tag (empty keeps ARTEX_TAG from .env, default latest)
  local tag; tag="$(ask 'Target image tag (Enter keeps .env / latest)' '')"
  if [ -n "$tag" ]; then
    if grep -q '^ARTEX_TAG=' .env; then
      sed -i.bak "s|^ARTEX_TAG=.*|ARTEX_TAG=${tag}|" .env && rm -f .env.bak
    else
      printf '\nARTEX_TAG=%s\n' "$tag" >> .env
    fi
    ok "set ARTEX_TAG to ${tag}"
  fi

  # Only touch artex: postgres stays pinned at 16-alpine and needs no upgrade (pulling it only wastes bandwidth,
  # and a major-version move risks incompatibility). artex declares depends_on postgres, so bringing it up by service name
  # auto-starts pg when it is down and leaves it alone (no rebuild) when it is already running.
  info "pulling the new image (artex only)..."
  docker compose pull artex
  info "rebuilding and starting (artex auto-migrates schema on restart)..."
  docker compose up -d artex
  ok "update done -> http://localhost:8787"
  info "view logs: docker compose logs -f artex"
  info "clean up old images (optional): docker image prune -f"
}

# ── 2. Local compile update ──────────────────────────────
update_local(){
  command -v go >/dev/null 2>&1 || die "Go not found (>=1.26): https://go.dev/dl/"
  [ -f config.json ] || warn "config.json not found — for first-time setup use ./install.sh instead"
  ok "Go: $(go version)"

  if command -v npm >/dev/null 2>&1; then
    info "rebuilding the static frontend bundle..."
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "recompiling the embedded single binary..."
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "npm not found: building the backend WITHOUT the embedded frontend (run the frontend separately via npm run dev)"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "build done -> ./artex"
  warn "restart the running artex process to take effect (schema auto-migrates on restart)"
}

echo "=============================="
echo "  ARTEX update"
echo "  1) Docker update (pull new image, rebuild)"
echo "  2) Local update (recompile with go)"
echo "=============================="
case "$(ask 'Choice' 1)" in
  1) sync_repo; update_docker ;;
  2) sync_repo; update_local ;;
  *) die "invalid choice" ;;
esac
