#!/bin/sh
# Container entry point: the control plane and the web app it serves.
#
# The control plane owns the port; the Next process is a helper it proxies pages
# to. One origin means the WebSocket endpoints for the terminal and browser
# panels need no special routing.
set -eu

DATA="${PIFORGE_DATA:-/data}"
PORT="${PIFORGE_PORT:-7700}"
mkdir -p "$DATA"

echo "piforge: starting web app on 127.0.0.1:3100"
cd /app/web
PORT=3100 HOSTNAME=127.0.0.1 node node_modules/next/dist/bin/next start -p 3100 &
WEB_PID=$!
cd /

# The image may or may not carry Chromium (it is an opt-in build arg), so detect it
# instead of baking a path that a slim image does not have.
CHROME="${PIFORGE_CHROME:-}"
if [ -z "$CHROME" ] && [ -x /usr/bin/chromium ]; then
  CHROME=/usr/bin/chromium
fi

echo "piforge: starting control plane on :$PORT (data=$DATA, chrome=${CHROME:-none})"
/app/bin/piforged \
  -addr "0.0.0.0:$PORT" \
  -data "$DATA" \
  -web-upstream "http://127.0.0.1:3100" \
  -pi "${PIFORGE_PI:-pi}" \
  -default-provider "${PIFORGE_DEFAULT_PROVIDER:-shared-anthropic}" \
  -gateway-url "${PIFORGE_GATEWAY_URL:-http://127.0.0.1:$PORT}" \
  -isolation "${PIFORGE_ISOLATION:-none}" \
  ${PIFORGE_BROWSER_PROXY:+-browser-proxy "$PIFORGE_BROWSER_PROXY"} \
  ${CHROME:+-chrome "$CHROME"} \
  ${PIFORGE_EXTRA_ARGS:-} &
CONTROL_PID=$!

# The control plane is the reason the container exists: when it exits, so do we.
wait "$CONTROL_PID"
STATUS=$?
echo "piforge: control plane exited with $STATUS"
kill "$WEB_PID" 2>/dev/null || true
exit "$STATUS"