#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

mkdir -p "$TMP_DIR/scripts" "$TMP_DIR/bin" "$TMP_DIR/cmd/proxy"
cp "$ROOT_DIR/scripts/uninstall-linux.sh" "$TMP_DIR/scripts/uninstall-linux.sh"
printf 'binary' > "$TMP_DIR/bin/openai-compat-proxy"
printf 'log' > "$TMP_DIR/.proxy.log"
sleep 30 &
PID=$!
echo "$PID" > "$TMP_DIR/.proxy.pid"

bash "$TMP_DIR/scripts/uninstall-linux.sh"

if kill -0 "$PID" 2>/dev/null; then
  echo "expected process to be stopped"
  exit 1
fi

if [[ -e "$TMP_DIR/.proxy.pid" || -e "$TMP_DIR/.proxy.log" || -e "$TMP_DIR/bin/openai-compat-proxy" ]]; then
  echo "expected runtime artifacts to be removed"
  exit 1
fi

if [[ ! -d "$TMP_DIR/cmd" ]]; then
  echo "expected project directory to remain"
  exit 1
fi

echo "uninstall script test passed"
