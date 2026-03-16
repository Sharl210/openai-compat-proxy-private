#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

mkdir -p "$TMP_DIR/scripts"
cp "$ROOT_DIR/scripts/restart-linux.sh" "$TMP_DIR/scripts/restart-linux.sh"

cat > "$TMP_DIR/scripts/uninstall-linux.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'uninstall\n' >> "__TMP_DIR__/calls.log"
EOF

cat > "$TMP_DIR/scripts/deploy-linux.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'deploy\n' >> "__TMP_DIR__/calls.log"
EOF

TMP_DIR="$TMP_DIR" python3 - <<'PY'
import os
from pathlib import Path
tmp = Path(os.environ["TMP_DIR"])
for path in (tmp / "scripts").iterdir():
    if path.name == "restart-linux.sh":
        continue
    path.write_text(path.read_text().replace("__TMP_DIR__", str(tmp)))
    path.chmod(0o755)
PY

bash "$TMP_DIR/scripts/restart-linux.sh"

if [[ ! -f "$TMP_DIR/calls.log" ]]; then
  echo "expected restart script to call uninstall and deploy"
  exit 1
fi

EXPECTED=$'uninstall\ndeploy\n'
ACTUAL="$(cat "$TMP_DIR/calls.log")"
if [[ "$ACTUAL"$'\n' != "$EXPECTED" ]]; then
  echo "expected uninstall then deploy, got: $ACTUAL"
  exit 1
fi

echo "restart script test passed"
