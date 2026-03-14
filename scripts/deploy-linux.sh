#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$ROOT_DIR/.env"
BIN_DIR="$ROOT_DIR/bin"
BIN_PATH="$BIN_DIR/openai-compat-proxy"
PID_FILE="$ROOT_DIR/.proxy.pid"
LOG_FILE="$ROOT_DIR/.proxy.log"

if [[ ! -f "$ENV_FILE" ]]; then
  echo "Missing .env. Copy .env.example to .env and fill required values before running this script." >&2
  exit 1
fi

set -a
source "$ENV_FILE"
set +a

: "${LISTEN_ADDR:?LISTEN_ADDR is required in .env}"
: "${UPSTREAM_BASE_URL:?UPSTREAM_BASE_URL is required in .env}"
: "${UPSTREAM_API_KEY:?UPSTREAM_API_KEY is required in .env}"

mkdir -p "$BIN_DIR"

go build -o "$BIN_PATH" ./cmd/proxy

if [[ -f "$PID_FILE" ]]; then
  OLD_PID="$(cat "$PID_FILE" 2>/dev/null || true)"
  if [[ -n "$OLD_PID" ]] && kill -0 "$OLD_PID" 2>/dev/null; then
    kill "$OLD_PID"
    sleep 1
  fi
fi

nohup env \
  LISTEN_ADDR="$LISTEN_ADDR" \
  UPSTREAM_BASE_URL="$UPSTREAM_BASE_URL" \
  UPSTREAM_API_KEY="$UPSTREAM_API_KEY" \
  PROXY_API_KEY="${PROXY_API_KEY:-}" \
  "$BIN_PATH" >"$LOG_FILE" 2>&1 &

NEW_PID=$!
echo "$NEW_PID" > "$PID_FILE"
sleep 1

PORT="${LISTEN_ADDR##*:}"
HEALTH_URL="http://127.0.0.1:${PORT}/healthz"
curl -fsS "$HEALTH_URL" >/dev/null

echo "deployed: $BIN_PATH"
echo "pid: $NEW_PID"
echo "health: $HEALTH_URL"
