#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

mkdir -p "$TMP_DIR/scripts" "$TMP_DIR/cmd/proxy" "$TMP_DIR/fakebin"
cp "$ROOT_DIR/scripts/deploy-linux.sh" "$TMP_DIR/scripts/deploy-linux.sh"
cat > "$TMP_DIR/.env" <<'EOF'
LISTEN_ADDR=:19083
UPSTREAM_BASE_URL=https://example.com/v1
UPSTREAM_API_KEY=test-key
EOF
cat > "$TMP_DIR/cmd/proxy/main.go" <<'EOF'
package main
func main() {}
EOF

cat > "$TMP_DIR/fakebin/go" <<'EOF'
#!/usr/bin/env bash
pwd > "__TMP_DIR__/pwd.log"
echo "$@" > "__TMP_DIR__/args.log"
if [[ "$1" == "build" ]]; then
  out=""
  while [[ $# -gt 0 ]]; do
    if [[ "$1" == "-o" ]]; then
      shift
      out="$1"
      break
    fi
    shift
  done
  mkdir -p "$(dirname "$out")"
  touch "$out"
fi
exit 0
EOF
cat > "$TMP_DIR/fakebin/curl" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat > "$TMP_DIR/fakebin/nohup" <<'EOF'
#!/usr/bin/env bash
shift
"$@" &
EOF

TMP_DIR="$TMP_DIR" python - <<'PY'
import os
from pathlib import Path
tmp = Path(os.environ['TMP_DIR'])
for path in (tmp / 'fakebin').iterdir():
    path.write_text(path.read_text().replace('__TMP_DIR__', str(tmp)))
    path.chmod(0o755)
PY

export PATH="$TMP_DIR/fakebin:/usr/bin:/bin"

set +e
OUTPUT="$(cd "$TMP_DIR/scripts" && bash ./deploy-linux.sh 2>&1)"
STATUS=$?
set -e

if [[ $STATUS -ne 0 ]]; then
  echo "expected deploy script success, got: $OUTPUT"
  exit 1
fi

if [[ "$(cat "$TMP_DIR/pwd.log")" != "$TMP_DIR" ]]; then
  echo "expected go build to run from project root"
  exit 1
fi

if [[ "$(cat "$TMP_DIR/args.log")" != *"./cmd/proxy"* ]]; then
  echo "expected go build args to include ./cmd/proxy"
  exit 1
fi

echo "deploy workdir test passed"
