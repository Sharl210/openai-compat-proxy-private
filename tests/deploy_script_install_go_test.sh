#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

mkdir -p "$TMP_DIR/scripts" "$TMP_DIR/bin"
cp "$ROOT_DIR/scripts/deploy-linux.sh" "$TMP_DIR/scripts/deploy-linux.sh"
cat > "$TMP_DIR/.env" <<'EOF'
LISTEN_ADDR=:19082
UPSTREAM_BASE_URL=https://example.com/v1
UPSTREAM_API_KEY=test-key
EOF

mkdir -p "$TMP_DIR/fakebin"
cat > "$TMP_DIR/fakebin/curl" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat > "$TMP_DIR/fakebin/nohup" <<'EOF'
#!/usr/bin/env bash
shift
"$@" &
EOF
cat > "$TMP_DIR/fakebin/apt-get" <<'EOF'
#!/usr/bin/env bash
echo "$@" >> "__TMP_DIR__/apt.log"
if [[ "$*" == *"install"* ]]; then
cat > "__TMP_DIR__/fakebin/go" <<'GOEOF'
#!/usr/bin/env bash
echo "$@" >> "__TMP_DIR__/go.log"
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
  touch "$out"
fi
exit 0
GOEOF
chmod +x "__TMP_DIR__/fakebin/go"
fi
exit 0
EOF

TMP_DIR="$TMP_DIR" python - <<'PY'
import os
from pathlib import Path
tmp = Path(os.environ["TMP_DIR"])
for path in (tmp / "fakebin").iterdir():
    path.write_text(path.read_text().replace("__TMP_DIR__", str(tmp)))
    path.chmod(0o755)
PY

export PATH="$TMP_DIR/fakebin:/usr/bin:/bin"
export DEPLOY_FORCE_INSTALL_GO=1

set +e
OUTPUT="$(bash "$TMP_DIR/scripts/deploy-linux.sh" 2>&1)"
STATUS=$?
set -e

if [[ $STATUS -ne 0 ]]; then
  echo "expected deploy script success, got: $OUTPUT"
  exit 1
fi

if [[ ! -f "$TMP_DIR/apt.log" ]]; then
  echo "expected apt-get to be used when go is missing"
  exit 1
fi

if [[ ! -f "$TMP_DIR/go.log" ]]; then
  echo "expected go build to run after installation"
  exit 1
fi

echo "deploy install go test passed"
