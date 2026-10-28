# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# 1. Web
# ---------------------------------------------------------------------------
FROM node:22-bookworm-slim AS web
# Registry and proxy are build-time inputs: a build box in China usually needs a
# mirror, and one behind a corporate proxy needs neither a mirror nor luck.
ARG NPM_REGISTRY=https://registry.npmmirror.com
ENV npm_config_registry=${NPM_REGISTRY} \
    npm_config_fetch_retries=6 \
    npm_config_fetch_retry_maxtimeout=120000 \
    npm_config_fetch_timeout=180000 \
    npm_config_network_concurrency=4
WORKDIR /src/web
RUN corepack enable
# .npmrc must be present for the install: it selects the hoisted layout, and
# without it Next cannot resolve its SWC helpers from a container.
COPY web/package.json web/pnpm-lock.yaml web/.npmrc ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
ENV NEXT_TELEMETRY_DISABLED=1
RUN pnpm exec next build

# ---------------------------------------------------------------------------
# 2. Control plane
# ---------------------------------------------------------------------------
FROM golang:1.25-bookworm AS backend
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY} GOFLAGS=-mod=mod
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# modernc.org/sqlite is pure Go, so no cgo toolchain is required.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/piforged ./cmd/piforged \
 && go build -trimpath -ldflags="-s -w" -o /out/fakepi ./cmd/fakepi

# ---------------------------------------------------------------------------
# 3. Runtime
#
# One container runs the control plane, the web app and the sandbox itself: the
# local sandbox provider starts processes next to the control plane, so the pod
# *is* the sandbox boundary. That is why git and the agent loop live here.
#
# The base is the Go image because it already ships git and a shell — the two
# things the agent cannot work without — and Node is copied in from the stage
# above instead of being installed twice.
# ---------------------------------------------------------------------------
FROM golang:1.25-bookworm AS runtime

# Debian packages come from a domestic mirror by default: deb.debian.org is often
# unreachable from a CN build box, and piping apt through a TLS-intercepting proxy
# fails signature verification. Single-connection downloads because some links drop
# long parallel transfers.
ARG APT_MIRROR=http://mirrors.aliyun.com
RUN set -e; \
    sed -i "s|http://deb.debian.org/debian|${APT_MIRROR}/debian|g; s|http://deb.debian.org/debian-security|${APT_MIRROR}/debian-security|g" \
      /etc/apt/sources.list.d/debian.sources; \
    printf 'Acquire::http::Pipeline-Depth "0";\nAcquire::http::Retries "8";\nAcquire::http::Timeout "60";\nAcquire::Queue-Mode "access";\n' \
      > /etc/apt/apt.conf.d/99net; \
    apt-get update -qq; \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      ca-certificates curl tini \
    ; rm -rf /var/lib/apt/lists/*

# Chromium is opt-in: it is ~250 MB of packages, and on a flaky link the transfer
# keeps dropping. With it, the browser panel works; without it, the panel reports
# itself unavailable and everything else is unaffected.
ARG INSTALL_CHROMIUM=false
RUN if [ "$INSTALL_CHROMIUM" = "true" ]; then \
      apt-get update -qq; \
      for attempt in 1 2 3 4 5 6; do \
        if DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends --fix-missing chromium; then \
          chromium --version; rm -rf /var/lib/apt/lists/*; exit 0; \
        fi; \
        echo "chromium download interrupted, retrying ($attempt)"; sleep 5; \
      done; \
      echo "chromium could not be downloaded"; exit 1; \
    fi

# The base already carries git, curl and bash — the tools the agent needs.
COPY --from=web /usr/local/bin/node /usr/local/bin/node
COPY --from=web /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -sf /usr/local/lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm

# The agent loop, pinned so a container that worked yesterday still works today.
ARG PI_VERSION=1.4.0
RUN npm install -g "@earendil-works/pi-coding-agent@${PI_VERSION}" \
 && pi --version

WORKDIR /app
COPY --from=backend /out/piforged /app/bin/piforged
COPY --from=backend /out/fakepi /app/bin/fakepi
COPY --from=web /src/web/.next /app/web/.next
COPY --from=web /src/web/public /app/web/public
COPY --from=web /src/web/package.json /app/web/package.json
COPY --from=web /src/web/node_modules /app/web/node_modules
COPY deploy/entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh && mkdir -p /data

# One port: the control plane serves /v1 and /gw itself and proxies the pages to
# the Next process, so there is no extra reverse proxy in the deployment.
ENV NODE_ENV=production \
    NEXT_TELEMETRY_DISABLED=1 \
    PIFORGE_DATA=/data \
    PIFORGE_PORT=7700 \
    PIFORGE_CHROME=""

VOLUME ["/data"]
EXPOSE 7700

# The entry point runs the control plane and the web app; `docker run --init`
# (or compose `init: true`) provides the reaper for the shells and agent
# processes they spawn.
ENTRYPOINT ["/usr/bin/tini", "--", "/app/entrypoint.sh"]
