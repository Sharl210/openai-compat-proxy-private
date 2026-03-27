#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
source "$ROOT_DIR/scripts/lib/runtime.sh"

acquire_lock
load_env
prepare_runtime_dependencies
stop_managed_service "$(extract_port "$LISTEN_ADDR")"
rm -f "$LOG_FILE" "$BIN_PATH" "$BACKUP_BIN_PATH" "$TMP_BIN_PATH"

echo "stopped and cleaned runtime artifacts"
