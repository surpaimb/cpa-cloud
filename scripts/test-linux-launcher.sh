#!/usr/bin/env bash
set -euo pipefail

source_root=$(cd "$(dirname "$0")/.." && pwd -P)
launcher="$source_root/packaging/linux/cpa-cloud-launcher"
temp_base=${TMPDIR:-/tmp}
temp_base=${temp_base%/}
stage_root=$(mktemp -d "$temp_base/cpa-cloud-linux-launcher-test.XXXXXXXX")

cleanup() {
  case "$stage_root" in
    "$temp_base"/cpa-cloud-linux-launcher-test.*) rm -rf -- "$stage_root" ;;
    *) echo "refusing to remove unsafe test path: $stage_root" >&2 ;;
  esac
}
trap cleanup EXIT

appdir="$stage_root/appdir"
mock_bin="$stage_root/mock-bin"
config_root="$stage_root/config"
mkdir -p "$appdir/usr/lib/cpa-cloud/web" "$mock_bin" "$config_root"
printf '<!doctype html><title>CPA Cloud test</title>\n' > "$appdir/usr/lib/cpa-cloud/web/index.html"

cat > "$appdir/usr/lib/cpa-cloud/cpa-cloud" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
for argument in "$@"; do
  case "$argument" in
    --check-initialized) exit 0 ;;
    --init) exit 0 ;;
  esac
done
instance_id=""
while (($#)); do
  if [[ "$1" == "--instance-id" ]]; then
    instance_id=${2-}
    break
  fi
  shift
done
[[ -n "$instance_id" ]]
if [[ "${MOCK_SERVER_MODE:-running}" == "exit" ]]; then
  exit 1
fi
printf '%s\n' "$instance_id" > "$MOCK_INSTANCE_FILE"
trap 'exit 0' TERM INT
while [[ ! -e "$MOCK_STOP_FILE" ]]; do /bin/sleep 0.02; done
EOF
chmod 755 "$appdir/usr/lib/cpa-cloud/cpa-cloud"

cat > "$mock_bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "${MOCK_HEALTH_MODE:-wrong}" in
  matching)
    for ((attempt = 0; attempt < 100; attempt++)); do
      if [[ -s "$MOCK_INSTANCE_FILE" ]]; then
        instance_id=$(<"$MOCK_INSTANCE_FILE")
        printf '{"instance_id":"%s","status":"ok"}\n' "$instance_id"
        exit 0
      fi
      /bin/sleep 0.01
    done
    exit 1
    ;;
  wrong) printf '{"instance_id":"00000000-0000-0000-0000-000000000000","status":"ok"}\n' ;;
  *) exit 1 ;;
esac
EOF

cat > "$mock_bin/xdg-open" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$1" > "$MOCK_BROWSER_MARKER"
touch "$MOCK_STOP_FILE"
EOF

cat > "$mock_bin/sleep" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF

cat > "$mock_bin/flock" <<'EOF'
#!/usr/bin/env bash
[[ "${MOCK_FLOCK_FAIL:-0}" != "1" ]]
EOF

cat > "$mock_bin/cat" <<'EOF'
#!/usr/bin/env bash
if [[ "${1-}" == "/proc/sys/kernel/random/uuid" ]]; then
  printf '123e4567-e89b-12d3-a456-426614174000\n'
  exit 0
fi
exec /bin/cat "$@"
EOF
chmod 755 "$mock_bin/curl" "$mock_bin/xdg-open" "$mock_bin/sleep" "$mock_bin/flock" "$mock_bin/cat"

export APPDIR="$appdir"
export XDG_CONFIG_HOME="$config_root"
export CPA_CLOUD_TERMINAL_HOSTED=1
export MOCK_INSTANCE_FILE="$stage_root/instance-id"
export MOCK_STOP_FILE="$stage_root/stop"
export MOCK_BROWSER_MARKER="$stage_root/browser-opened"

reset_case() {
  rm -f "$MOCK_INSTANCE_FILE" "$MOCK_STOP_FILE" "$MOCK_BROWSER_MARKER"
}

assert_contains() {
  local pattern=$1 file=$2
  if ! grep -q "$pattern" "$file"; then
    echo "expected '$pattern' in $file:" >&2
    /bin/cat "$file" >&2
    exit 1
  fi
}

no_curl_bin="$stage_root/no-curl-bin"
mkdir -p "$no_curl_bin"
cp "$mock_bin/flock" "$no_curl_bin/flock"
if PATH="$no_curl_bin" /bin/bash "$launcher" >"$stage_root/no-curl.out" 2>"$stage_root/no-curl.err"; then
  echo "launcher accepted an environment without curl" >&2
  exit 1
fi
assert_contains 'curl is required' "$stage_root/no-curl.err"
[[ ! -e "$MOCK_BROWSER_MARKER" ]]

export MOCK_FLOCK_FAIL=1
if PATH="$mock_bin:$PATH" /bin/bash "$launcher" >"$stage_root/locked.out" 2>"$stage_root/locked.err"; then
  echo "launcher accepted a conflicting lock" >&2
  exit 1
fi
assert_contains 'no browser was opened' "$stage_root/locked.err"
[[ ! -e "$MOCK_BROWSER_MARKER" ]]
export MOCK_FLOCK_FAIL=0

reset_case
export MOCK_HEALTH_MODE=wrong
export MOCK_SERVER_MODE=running
if PATH="$mock_bin:$PATH" /bin/bash "$launcher" >"$stage_root/wrong-health.out" 2>"$stage_root/wrong-health.err"; then
  echo "launcher accepted a health response for another instance" >&2
  exit 1
fi
assert_contains 'service did not become ready' "$stage_root/wrong-health.err"
[[ ! -e "$MOCK_BROWSER_MARKER" ]]

reset_case
export MOCK_HEALTH_MODE=matching
export MOCK_SERVER_MODE=exit
if PATH="$mock_bin:$PATH" /bin/bash "$launcher" >"$stage_root/occupied.out" 2>"$stage_root/occupied.err"; then
  echo "launcher accepted a child that failed before owning the port" >&2
  exit 1
fi
assert_contains 'service did not become ready' "$stage_root/occupied.err"
[[ ! -e "$MOCK_BROWSER_MARKER" ]]

reset_case
export MOCK_HEALTH_MODE=matching
export MOCK_SERVER_MODE=running
PATH="$mock_bin:$PATH" /bin/bash "$launcher" >"$stage_root/success.out" 2>"$stage_root/success.err"
grep -q '^http://127.0.0.1:8787/admin/$' "$MOCK_BROWSER_MARKER"

echo "Linux launcher ownership checks passed."
