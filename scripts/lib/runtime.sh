#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="${ROOT_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
ENV_FILE="${ENV_FILE:-$ROOT_DIR/.env}"
GO_MOD_FILE="${GO_MOD_FILE:-$ROOT_DIR/go.mod}"
BIN_DIR="${BIN_DIR:-$ROOT_DIR/bin}"
BIN_PATH="${BIN_PATH:-$BIN_DIR/openai-compat-proxy}"
TMP_BIN_PATH="${TMP_BIN_PATH:-$BIN_DIR/openai-compat-proxy.new}"
BACKUP_BIN_PATH="${BACKUP_BIN_PATH:-$BIN_DIR/openai-compat-proxy.bak}"
PID_FILE="${PID_FILE:-$ROOT_DIR/.proxy.pid}"
LOG_FILE="${LOG_FILE:-$ROOT_DIR/.proxy.log}"
LOCK_DIR="${LOCK_DIR:-$ROOT_DIR/.proxy.lock}"
GO_PROFILE_FILE="${GO_PROFILE_FILE:-/etc/profile.d/go.sh}"

GO_BIN="${GO_BIN:-}"
LOCK_ACQUIRED=0

log() {
  printf '%s\n' "$*"
}

fail() {
  printf '%s\n' "$*" >&2
  exit 1
}

acquire_lock() {
  if mkdir "$LOCK_DIR" 2>/dev/null; then
    LOCK_ACQUIRED=1
    trap release_lock EXIT
    return 0
  fi
  fail "another deploy/restart/stop operation is already running"
}

release_lock() {
  if [[ "$LOCK_ACQUIRED" == "1" ]]; then
    rm -rf "$LOCK_DIR"
    LOCK_ACQUIRED=0
  fi
}

command_missing() {
  ! command -v "$1" >/dev/null 2>&1
}

ensure_packages_available() {
  local missing=()
  local pkg
  for pkg in "$@"; do
    if command_missing "$pkg"; then
      missing+=("$pkg")
    fi
  done
  if [[ ${#missing[@]} -eq 0 ]]; then
    return 0
  fi
  if ! command -v apt-get >/dev/null 2>&1; then
    fail "missing required commands: ${missing[*]}"
  fi
  apt-get update
  local apt_packages=()
  for pkg in "${missing[@]}"; do
    case "$pkg" in
      curl) apt_packages+=(curl ca-certificates) ;;
      python3) apt_packages+=(python3) ;;
      tar) apt_packages+=(tar) ;;
      sha256sum|sort|awk) apt_packages+=(coreutils gawk) ;;
      ss) apt_packages+=(iproute2) ;;
      ps) apt_packages+=(procps) ;;
      *) apt_packages+=("$pkg") ;;
    esac
  done
  apt-get install -y "${apt_packages[@]}"
}

require_env_file() {
  [[ -f "$ENV_FILE" ]] || fail "Missing .env. Copy .env.example to .env and fill required values before running this script."
}

load_env() {
  require_env_file
  set -a
  source "$ENV_FILE"
  set +a
  : "${LISTEN_ADDR:?LISTEN_ADDR is required in .env}"
  : "${PROVIDERS_DIR:?PROVIDERS_DIR is required in .env}"
}

validate_provider_inputs() {
  [[ -d "$PROVIDERS_DIR" ]] || fail "PROVIDERS_DIR does not exist: $PROVIDERS_DIR"
  compgen -G "$PROVIDERS_DIR/*.env" >/dev/null || fail "PROVIDERS_DIR must contain at least one provider .env file: $PROVIDERS_DIR"
}

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
  ensure_packages_available curl python3 tar sha256sum sort awk
  local resolved
  mapfile -t resolved < <(resolve_latest_stable_go_release)
  local version="${resolved[0]}"
  local filename="${resolved[1]}"
  local sha256_expected="${resolved[2]}"
  local url="https://go.dev/dl/${filename}"
  local tmpdir
  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' RETURN

  curl -fsSL "$url" -o "$tmpdir/go.tgz"
  local sha256_actual
  sha256_actual="$(sha256sum "$tmpdir/go.tgz" | awk '{print $1}')"
  [[ "$sha256_actual" == "$sha256_expected" ]] || fail "Downloaded Go tarball checksum mismatch for ${filename}."

  rm -rf /usr/local/go
  tar -C /usr/local -xzf "$tmpdir/go.tgz"
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
  ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
  export PATH="/usr/local/go/bin:$PATH"
  persist_go_path
  GO_BIN="/usr/local/go/bin/go"
  log "installed go ${version} from official tarball"
}

ensure_go() {
  [[ -f "$GO_MOD_FILE" ]] || fail "Missing go.mod; cannot determine required Go version."
  local required_minor
  required_minor="$(awk '/^go / {print $2; exit}' "$GO_MOD_FILE")"
  [[ -n "$required_minor" ]] || fail "Failed to parse required Go version from go.mod."

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

  install_go_from_official_tarball

  local installed_version
  installed_version="$($GO_BIN env GOVERSION 2>/dev/null || $GO_BIN version | awk '{print $3}')"
  version_satisfies "$installed_version" "$required_minor" || fail "Installed Go version ${installed_version} does not satisfy required ${required_minor}."
}

extract_port() {
  local listen_addr="$1"
  [[ "$listen_addr" =~ :([0-9]+)$ ]] || fail "LISTEN_ADDR must end with a numeric TCP port: $listen_addr"
  printf '%s\n' "${BASH_REMATCH[1]}"
}

extract_host() {
  local listen_addr="$1"
  if [[ "$listen_addr" =~ ^:([0-9]+)$ ]]; then
    printf '\n'
    return 0
  fi
  if [[ "$listen_addr" =~ ^\[([^]]+)\]:([0-9]+)$ ]]; then
    printf '%s\n' "${BASH_REMATCH[1]}"
    return 0
  fi
  if [[ "$listen_addr" =~ ^([^:]+):([0-9]+)$ ]]; then
    printf '%s\n' "${BASH_REMATCH[1]}"
    return 0
  fi
  fail "LISTEN_ADDR must be in host:port form: $listen_addr"
}

pid_exists() {
  local pid="$1"
  [[ -n "$pid" ]] || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  local state=""
  state="$(ps -o stat= -p "$pid" 2>/dev/null | awk '{print $1}' || true)"
  [[ "$state" != Z* ]]
}

pid_matches_service() {
  local pid="$1"
  pid_exists "$pid" || return 1
  local exe=""
  exe="$(readlink "/proc/$pid/exe" 2>/dev/null || true)"
  [[ "$exe" == "$BIN_PATH" ]] && return 0
  local cmdline=""
  cmdline="$(tr '\0' ' ' < "/proc/$pid/cmdline" 2>/dev/null || true)"
  [[ "$cmdline" == *"$BIN_PATH"* ]]
}

listener_pids_for_port() {
  local port="$1"
  command -v ss >/dev/null 2>&1 || return 0
  ss -ltnpH "( sport = :$port )" 2>/dev/null | grep -oE 'pid=[0-9]+' | cut -d= -f2 | sort -u || true
}

port_has_any_listener() {
  local port="$1"
  local pid
  while IFS= read -r pid; do
    [[ -n "$pid" ]] && return 0
  done < <(listener_pids_for_port "$port")
  return 1
}

port_has_service_listener() {
  local port="$1"
  local pid
  while IFS= read -r pid; do
    [[ -n "$pid" ]] || continue
    if pid_matches_service "$pid"; then
      return 0
    fi
  done < <(listener_pids_for_port "$port")
  return 1
}

port_has_pid_listener() {
  local port="$1"
  local expected_pid="$2"
  local pid
  while IFS= read -r pid; do
    [[ "$pid" == "$expected_pid" ]] && return 0
  done < <(listener_pids_for_port "$port")
  return 1
}

wait_for_pid_exit() {
  local pid="$1"
  local timeout_seconds="$2"
  local deadline=$((SECONDS + timeout_seconds))
  while pid_exists "$pid"; do
    (( SECONDS < deadline )) || return 1
    sleep 0.2
  done
  return 0
}

wait_for_port_free() {
  local port="$1"
  local timeout_seconds="$2"
  local deadline=$((SECONDS + timeout_seconds))
  while port_has_any_listener "$port"; do
    (( SECONDS < deadline )) || return 1
    sleep 0.2
  done
  return 0
}

stop_pid_gracefully() {
  local pid="$1"
  pid_matches_service "$pid" || return 0
  kill "$pid" 2>/dev/null || true
  if wait_for_pid_exit "$pid" 5; then
    return 0
  fi
  kill -KILL "$pid" 2>/dev/null || true
  wait_for_pid_exit "$pid" 5 || fail "failed to stop process $pid"
}

stop_managed_service() {
  local port="$1"
  local pid=""
  if [[ -f "$PID_FILE" ]]; then
    pid="$(tr -d '[:space:]' < "$PID_FILE" 2>/dev/null || true)"
    if [[ -n "$pid" ]] && pid_matches_service "$pid"; then
      stop_pid_gracefully "$pid"
    fi
  fi

  local listener_pid
  while IFS= read -r listener_pid; do
    [[ -n "$listener_pid" ]] || continue
    if pid_matches_service "$listener_pid"; then
      stop_pid_gracefully "$listener_pid"
    fi
  done < <(listener_pids_for_port "$port")

  wait_for_port_free "$port" 5 || fail "service port $port is occupied by another process"
  rm -f "$PID_FILE"
}

prepare_runtime_dependencies() {
  ensure_packages_available curl python3 tar sha256sum sort awk ss ps
}

preflight_deploy() {
  load_env
  validate_provider_inputs
  prepare_runtime_dependencies
  ensure_go
  mkdir -p "$BIN_DIR"
}

build_candidate_binary() {
  rm -f "$TMP_BIN_PATH"
  "$GO_BIN" build -o "$TMP_BIN_PATH" ./cmd/proxy
}

install_candidate_binary() {
  if [[ -f "$BIN_PATH" ]]; then
    cp "$BIN_PATH" "$BACKUP_BIN_PATH"
  else
    rm -f "$BACKUP_BIN_PATH"
  fi
  mv "$TMP_BIN_PATH" "$BIN_PATH"
  chmod +x "$BIN_PATH"
}

append_log_banner() {
  mkdir -p "$(dirname "$LOG_FILE")"
  printf '\n===== %s =====\n' "$1" >> "$LOG_FILE"
}

health_url() {
  local port="$1"
  local host
  host="$(extract_host "$LISTEN_ADDR")"
  case "$host" in
    ""|"0.0.0.0"|"::") host="127.0.0.1" ;;
  esac
  if [[ "$host" == *:* ]]; then
    printf 'http://[%s]:%s/healthz\n' "$host" "$port"
    return 0
  fi
  printf 'http://%s:%s/healthz\n' "$host" "$port"
}

start_service() {
  local port="$1"
  append_log_banner "starting $(date -u +%Y-%m-%dT%H:%M:%SZ)"
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
    "$BIN_PATH" >>"$LOG_FILE" 2>&1 &
  local new_pid=$!
  echo "$new_pid" > "$PID_FILE"

  local deadline=$((SECONDS + 15))
  local url
  url="$(health_url "$port")"
  while (( SECONDS < deadline )); do
    if ! pid_exists "$new_pid"; then
      break
    fi
    if port_has_pid_listener "$port" "$new_pid"; then
      if curl -fsS --max-time 2 "$url" >/dev/null; then
        log "deployed: $BIN_PATH"
        log "pid: $new_pid"
        log "health: $url"
        return 0
      fi
    fi
    sleep 0.5
  done

  rm -f "$PID_FILE"
  if pid_exists "$new_pid"; then
    kill -KILL "$new_pid" 2>/dev/null || true
    wait_for_pid_exit "$new_pid" 5 || true
  fi
  return 1
}

rollback_binary() {
  if [[ -f "$BACKUP_BIN_PATH" ]]; then
    mv "$BACKUP_BIN_PATH" "$BIN_PATH"
  fi
}

deploy_service() {
  preflight_deploy
  local port
  port="$(extract_port "$LISTEN_ADDR")"
  build_candidate_binary
  local had_running_service=0
  if port_has_service_listener "$port" || ([[ -f "$PID_FILE" ]] && pid_matches_service "$(tr -d '[:space:]' < "$PID_FILE" 2>/dev/null || true)"); then
    had_running_service=1
  fi
  stop_managed_service "$port"
  install_candidate_binary
  if start_service "$port"; then
    rm -f "$BACKUP_BIN_PATH"
    return 0
  fi
  rollback_binary
  if [[ "$had_running_service" == "1" ]] && [[ -x "$BIN_PATH" ]]; then
    append_log_banner "rollback $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    if start_service "$port"; then
      fail "new deployment failed; rolled back to previous binary"
    fi
  fi
  fail "deployment failed; service did not become healthy"
}

restart_service() {
  load_env
  prepare_runtime_dependencies
  local port
  port="$(extract_port "$LISTEN_ADDR")"
  stop_managed_service "$port"
  preflight_deploy
  build_candidate_binary
  install_candidate_binary
  if start_service "$port"; then
    rm -f "$BACKUP_BIN_PATH"
    return 0
  fi
  rollback_binary
  if [[ -x "$BIN_PATH" ]] && start_service "$port"; then
    fail "restart failed after stop; rolled back to previous binary"
  fi
  fail "restart failed; service did not become healthy"
}

stop_service_entry() {
  load_env
  prepare_runtime_dependencies
  local port
  port="$(extract_port "$LISTEN_ADDR")"
  stop_managed_service "$port"
  log "service stopped"
}
