#!/usr/bin/env bash
# Dev mode: backend (:8787) + traffic proxy (:8788) together with the frontend next dev (:5173).
# The frontend /api reverse-proxies to the backend; Ctrl-C exits everything.
#
# For the single-binary (embedded frontend) approach see the "single binary" section of the README — it does not use this script.
set -euo pipefail
cd "$(dirname "$0")"

# Kill all child processes in this process group on exit (backend + frontend).
cleanup() { kill 0 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# Backend (plain go run, frontend not embedded); concurrent work-agent count is configured under "System Settings".
go run ./cmd/artex -addr :8787 -proxy 127.0.0.1:8788 &

# Frontend hot reload (Vite/Next dev server, /api reverse-proxied to :8787).
( cd web && npm run dev ) &

echo "[dev] backend :8787 / proxy :8788 / frontend http://localhost:5173  (Ctrl-C to exit)"
wait
