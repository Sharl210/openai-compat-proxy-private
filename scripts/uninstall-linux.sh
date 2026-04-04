#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
source "$ROOT_DIR/scripts/lib/runtime.sh"

acquire_lock
prepare_runtime_dependencies
port=""
if load_env_if_present; then
  port="$(extract_port "$LISTEN_ADDR")"
fi
stop_managed_service "$port"
rm -f "$BIN_PATH" "$BACKUP_BIN_PATH" "$TMP_BIN_PATH"

echo "stopped and cleaned runtime artifacts"
