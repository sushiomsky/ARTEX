# syntax=docker/dockerfile:1
#
# Runtime image (no build inside the image): installs common tools plus the **prebuilt Linux single binary**.
# The binary is cross-compiled by the CI binaries job (pure Go, no QEMU) and placed under the
# build context at dist/<TARGETARCH>/artex. Multi-arch builds then only emulate the apt layer
# for arm64 instead of emulating Next/Go compilation — much faster.
#
# To build the image manually, prepare the binary first:
#   cd web && npm run build:static && cd ..
#   cp -r web/out server/webui/dist
#   CGO_ENABLED=0 GOARCH=amd64 go build -tags embedui -o dist/amd64/artex ./cmd/artex
#   docker build -t artex:local .
FROM python:3.12-slim-bookworm
ARG TARGETARCH
# Common tools: ripgrep / curl / vim, plus a set of recon staples (add/remove as needed).
# Node 20.x comes from NodeSource: bookworm's stock apt nodejs is 18, and Playwright requires >=20.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ripgrep curl wget vim git jq unzip \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
# Preinstall Playwright MCP and CLI (global) so the runtime never downloads over npx.
# @playwright/mcp: browser MCP runs directly as `npx @playwright/mcp` (already installed globally, no -y/@latest needed).
# @playwright/cli: provides playwright-cli; --help afterwards verifies it runs.
# Then install playwright (browser management) and preinstall chromium plus system deps with --with-deps,
# so MCP/CLI work on first launch inside the container with no browser download.
RUN npm install -g @playwright/mcp@latest @playwright/cli@latest playwright@latest \
    && playwright-cli --help \
    && playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
# Prebuilt binary for the matching arch (dist/amd64/artex or dist/arm64/artex)
COPY dist/${TARGETARCH}/artex /app/artex
# Supervisor launch script: relaunches the process based on its exit code; the one-click in-page update relies on it to swap in the new build.
# It also forwards SIGTERM to artex — docker stop only signals PID 1, and without forwarding
# artex never receives it, misses graceful shutdown, and gets SIGKILLed after 10s.
COPY start.sh /app/start.sh
RUN chmod +x /app/artex /app/start.sh
COPY skills/ /app/skills/
# data/ (SQLite + jwt.key) persistence point
VOLUME ["/app/data"]
EXPOSE 8787 8788
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]
