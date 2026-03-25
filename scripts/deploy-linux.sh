#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

ENV_FILE="$ROOT_DIR/.env"
GO_MOD_FILE="$ROOT_DIR/go.mod"
BIN_DIR="$ROOT_DIR/bin"
BIN_PATH="$BIN_DIR/openai-compat-proxy"
PID_FILE="$ROOT_DIR/.proxy.pid"
LOG_FILE="$ROOT_DIR/.proxy.log"
GO_PROFILE_FILE="/etc/profile.d/go.sh"

GO_BIN=""

version_satisfies() {
  local current="$1"
  local required="$2"
  current="${current#go}"
  printf '%s\n%s\n' "$required" "$current" | sort -V -C
}

resolve_latest_stable_go_release() {
  python3 - <<'PY'
import json
import platform
import urllib.request

machine = platform.machine().lower()
arch_map = {
    "x86_64": "amd64",
    "amd64": "amd64",
    "aarch64": "arm64",
    "arm64": "arm64",
}
arch = arch_map.get(machine)
if not arch:
    raise SystemExit(f"unsupported architecture: {machine}")

with urllib.request.urlopen("https://go.dev/dl/?mode=json", timeout=20) as resp:
    releases = json.load(resp)

stable = None
for item in releases:
    if item.get("stable"):
        stable = item
        break

if not stable:
    raise SystemExit("unable to resolve stable Go release")

filename = f"{stable['version']}.linux-{arch}.tar.gz"
sha256 = None
for file_info in stable.get("files", []):
    if file_info.get("filename") == filename:
        sha256 = file_info.get("sha256")
        break

if not sha256:
    raise SystemExit(f"unable to find download metadata for {filename}")

print(stable["version"][2:])
print(filename)
print(sha256)
PY
}

persist_go_path() {
  if [[ -w /etc/profile.d ]] || [[ ! -e "$GO_PROFILE_FILE" && -w /etc/profile.d ]]; then
    cat > "$GO_PROFILE_FILE" <<'EOF'
export PATH=/usr/local/go/bin:$PATH
EOF
    chmod 0644 "$GO_PROFILE_FILE"
  fi
}

install_go_from_official_tarball() {
  local resolved
  mapfile -t resolved < <(resolve_latest_stable_go_release)
  local version="${resolved[0]}"
  local filename="${resolved[1]}"
  local sha256_expected="${resolved[2]}"
  local url="https://go.dev/dl/${filename}"
  local tmpdir
  tmpdir="$(mktemp -d)"
  trap "rm -rf '$tmpdir'" RETURN

  curl -fsSL "$url" -o "$tmpdir/go.tgz"
  local sha256_actual
  sha256_actual="$(sha256sum "$tmpdir/go.tgz" | awk '{print $1}')"
  if [[ "$sha256_actual" != "$sha256_expected" ]]; then
    echo "Downloaded Go tarball checksum mismatch for ${filename}." >&2
    exit 1
  fi

  rm -rf /usr/local/go
  tar -C /usr/local -xzf "$tmpdir/go.tgz"
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
  ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
  export PATH="/usr/local/go/bin:$PATH"
  persist_go_path
  GO_BIN="/usr/local/go/bin/go"
  echo "installed go ${version} from official tarball"
}

ensure_go() {
  if [[ ! -f "$GO_MOD_FILE" ]]; then
    echo "Missing go.mod; cannot determine required Go version." >&2
    exit 1
  fi

  local required_minor
  required_minor="$(awk '/^go / {print $2; exit}' "$GO_MOD_FILE")"
  if [[ -z "$required_minor" ]]; then
    echo "Failed to parse required Go version from go.mod." >&2
    exit 1
  fi

  local current_go_bin=""
  if command -v go >/dev/null 2>&1; then
    current_go_bin="$(command -v go)"
  elif [[ -x /usr/local/go/bin/go ]]; then
    current_go_bin="/usr/local/go/bin/go"
  fi

  if [[ "${DEPLOY_FORCE_INSTALL_GO:-}" != "1" ]] && [[ -n "$current_go_bin" ]]; then
    local current_version
    current_version="$($current_go_bin env GOVERSION 2>/dev/null || $current_go_bin version | awk '{print $3}')"
    if version_satisfies "$current_version" "$required_minor"; then
      GO_BIN="$current_go_bin"
      export PATH="$(dirname "$GO_BIN"):$PATH"
      return 0
    fi
  fi

  if ! command -v curl >/dev/null 2>&1; then
    if command -v apt-get >/dev/null 2>&1; then
      apt-get update
      apt-get install -y curl ca-certificates
    else
      echo "curl is required to install Go automatically." >&2
      exit 1
    fi
  fi

  install_go_from_official_tarball

  local installed_version
  installed_version="$($GO_BIN env GOVERSION 2>/dev/null || $GO_BIN version | awk '{print $3}')"
  if ! version_satisfies "$installed_version" "$required_minor"; then
    echo "Installed Go version ${installed_version} does not satisfy required ${required_minor}." >&2
    exit 1
  fi
}

if [[ ! -f "$ENV_FILE" ]]; then
  echo "Missing .env. Copy .env.example to .env and fill required values before running this script." >&2
  exit 1
fi

set -a
source "$ENV_FILE"
set +a

: "${LISTEN_ADDR:?LISTEN_ADDR is required in .env}"
: "${PROVIDERS_DIR:?PROVIDERS_DIR is required in .env}"

mkdir -p "$BIN_DIR"

ensure_go

"$GO_BIN" build -o "$BIN_PATH" ./cmd/proxy

if [[ -f "$PID_FILE" ]]; then
  OLD_PID="$(cat "$PID_FILE" 2>/dev/null || true)"
  if [[ -n "$OLD_PID" ]] && kill -0 "$OLD_PID" 2>/dev/null; then
    kill "$OLD_PID"
    sleep 1
  fi
fi

nohup env \
  LISTEN_ADDR="$LISTEN_ADDR" \
  PROXY_API_KEY="${PROXY_API_KEY:-}" \
  PROVIDERS_DIR="${PROVIDERS_DIR:-}" \
  DEFAULT_PROVIDER="${DEFAULT_PROVIDER:-}" \
  ENABLE_LEGACY_V1_ROUTES="${ENABLE_LEGACY_V1_ROUTES:-}" \
  CONNECT_TIMEOUT="${CONNECT_TIMEOUT:-}" \
  FIRST_BYTE_TIMEOUT="${FIRST_BYTE_TIMEOUT:-}" \
  IDLE_TIMEOUT="${IDLE_TIMEOUT:-}" \
  TOTAL_TIMEOUT="${TOTAL_TIMEOUT:-}" \
  LOG_ENABLE="${LOG_ENABLE:-}" \
  LOG_FILE_PATH="${LOG_FILE_PATH:-}" \
  LOG_INCLUDE_BODIES="${LOG_INCLUDE_BODIES:-}" \
  LOG_MAX_SIZE_MB="${LOG_MAX_SIZE_MB:-}" \
  LOG_MAX_BACKUPS="${LOG_MAX_BACKUPS:-}" \
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
