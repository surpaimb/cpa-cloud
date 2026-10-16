#!/usr/bin/env bash
# Real systemd activation acceptance using only disposable synthetic credentials.
set -euo pipefail

credential_name=cpa-cloud-master-key

if [[ ${1:-} == --inside-unit ]]; then
  metadata_path=$2
  shift 2
  python3 - "$CREDENTIALS_DIRECTORY" "$credential_name" "$metadata_path" <<'PY'
import os
import struct
import sys

directory, name, output = sys.argv[1:]
path = os.path.join(directory, name)

def acl_summary(target):
    raw = os.getxattr(target, "system.posix_acl_access")
    if len(raw) < 4 or (len(raw) - 4) % 8 != 0:
        raise SystemExit("malformed activation ACL")
    version, = struct.unpack_from("<I", raw)
    if version != 2:
        raise SystemExit("unexpected activation ACL version")
    return ",".join(
        f"{tag:x}:{perm:x}:{entry_id}"
        for tag, perm, entry_id in struct.iter_unpack("<HHI", raw[4:])
    )

directory_stat = os.stat(directory, follow_symlinks=False)
file_stat = os.stat(path, follow_symlinks=False)
with open(output, "w", encoding="ascii") as stream:
    stream.write(f"directory={directory_stat.st_uid}:{directory_stat.st_gid}:{directory_stat.st_mode & 0o7777:o}\n")
    stream.write(f"directory_acl={acl_summary(directory)}\n")
    stream.write(f"file={file_stat.st_uid}:{file_stat.st_gid}:{file_stat.st_mode & 0o7777:o}:{file_stat.st_nlink}\n")
    stream.write(f"file_acl={acl_summary(path)}\n")
PY
  exec "$@"
fi

if [[ $# -ne 1 || ! -x $1 ]]; then
  echo "usage: $0 /absolute/path/to/cpa-cloud" >&2
  exit 2
fi

server=$(realpath "$1")
test_root=$(mktemp -d -p /var/tmp cpa-cloud-systemd-credential.XXXXXX)
test_user="cpacred${RANDOM}${RANDOM}"
unit_prefix="cpa-cloud-credential-ci-${RANDOM}${RANDOM}"
unit_index=0

cleanup() {
  sudo userdel "$test_user" >/dev/null 2>&1 || true
  case "$test_root" in
    /var/tmp/cpa-cloud-systemd-credential.*) sudo rm -rf -- "$test_root" ;;
  esac
}
trap cleanup EXIT

sudo useradd --system --user-group --no-create-home --shell /usr/sbin/nologin "$test_user"
test_uid=$(id -u "$test_user")
sudo chown root:root "$test_root"
sudo chmod 0755 "$test_root"
sudo install -o root -g root -m 0755 "$server" "$test_root/cpa-cloud"
sudo install -o root -g root -m 0755 "$0" "$test_root/smoke-linux-systemd-credential.sh"
sudo install -d -o "$test_user" -g "$test_user" -m 0700 "$test_root/state"

make_encrypted_credential() {
  local output=$1
  local marker=$2
  local plaintext="$test_root/plaintext-$marker.json"
  local key
  key=$(head -c 32 /dev/urandom | base64 -w0 | tr -d '=')
  umask 077
  printf '{"format_version":1,"provider_kind":"linux-systemd-credential-v1","instance_id":"01234567-89ab-cdef-0123-456789abcdef","key_id":"installation-root","key_version":1,"master_key":"%s"}' "$key" >"$plaintext"
  sudo systemd-creds encrypt --with-key=host --name="$credential_name" "$plaintext" "$output" >/dev/null
  rm -f -- "$plaintext"
}

run_unit() {
  local credential=$1
  shift
  unit_index=$((unit_index + 1))
  local unit="$unit_prefix-$unit_index.service"
  local -a args=(
    --wait --collect --quiet
    --unit="$unit"
    --property="User=$test_user"
    --property="Group=$test_user"
    --property=StandardOutput=null
  )
  if [[ -n $credential ]]; then
    args+=(--property="LoadCredentialEncrypted=$credential_name:$credential")
  fi
  sudo systemd-run "${args[@]}" "$@"
}

sudo systemd-creds setup >/dev/null
credential="$test_root/credential.encrypted"
wrong_credential="$test_root/wrong-credential.encrypted"
make_encrypted_credential "$credential" primary
make_encrypted_credential "$wrong_credential" wrong

metadata="$test_root/state/activation-metadata.txt"
run_unit "$credential" \
  --property=StandardInput=data \
  --property=StandardInputText=synthetic-admin-password \
  /bin/bash "$test_root/smoke-linux-systemd-credential.sh" --inside-unit "$metadata" \
  "$test_root/cpa-cloud" --data-dir "$test_root/state/data" --master-key-provider=linux-systemd-credential-v1 --init

sudo grep -qx 'directory=0:0:550' "$metadata"
sudo grep -qx 'file=0:0:440:1' "$metadata"
sudo grep -Eq "^directory_acl=1:5:4294967295,2:5:${test_uid},4:0:4294967295,10:5:4294967295,20:0:4294967295$" "$metadata"
sudo grep -Eq "^file_acl=1:4:4294967295,2:4:${test_uid},4:0:4294967295,10:4:4294967295,20:0:4294967295$" "$metadata"
sudo test -f "$test_root/state/data/master-key-binding.json"
sudo test -f "$test_root/state/data/cpa-cloud.db"
sudo test ! -e "$test_root/state/data/master.key"

run_unit "$credential" "$test_root/cpa-cloud" --data-dir "$test_root/state/data" --master-key-provider=linux-systemd-credential-v1 --check-initialized
if run_unit "$wrong_credential" "$test_root/cpa-cloud" --data-dir "$test_root/state/data" --master-key-provider=linux-systemd-credential-v1 --check-initialized >/dev/null 2>&1; then
  echo "wrong credential was accepted" >&2
  exit 1
fi
if run_unit "" "$test_root/cpa-cloud" --data-dir "$test_root/state/data" --master-key-provider=linux-systemd-credential-v1 --check-initialized >/dev/null 2>&1; then
  echo "missing credential was accepted" >&2
  exit 1
fi

echo "PASS: fixed User= LoadCredentialEncrypted activation, exact ACL admission, init/reopen, and wrong/missing rejection"
