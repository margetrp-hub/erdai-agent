#!/bin/sh
set -eu

root=${ERDAI_INSTALL_ROOT:-/opt/erdai-agent}
request_file=${ERDAI_UPDATE_REQUEST_FILE:-$root/data/update-request.json}
status_file=${ERDAI_UPDATE_STATUS_FILE:-$root/update-status/update-status.json}
work_root=${ERDAI_UPDATE_WORK_DIR:-$root/data/update-work}
poll_seconds=${ERDAI_UPDATE_POLL_SECONDS:-5}
allowed_repository=${ERDAI_UPDATE_REPOSITORY:-}
running_script_digest=$(sha256sum "$0" | awk '{print $1}')

case "$poll_seconds" in *[!0-9]*|'') poll_seconds=5 ;; esac
[ "$poll_seconds" -ge 1 ] || poll_seconds=1

for command in curl python3 sha256sum tar; do
  command -v "$command" >/dev/null || { echo "$command is required" >&2; exit 1; }
done

timestamp() { date -u +%Y-%m-%dT%H:%M:%SZ; }

safe_extract_archive() {
  archive=$1
  target=$2
  python3 - "$archive" "$target" <<'PY'
import sys
import tarfile
from pathlib import Path, PurePosixPath

archive = Path(sys.argv[1])
target = Path(sys.argv[2])
with tarfile.open(archive, "r:gz") as bundle:
    members = bundle.getmembers()
    for member in members:
        path = PurePosixPath(member.name)
        if path.is_absolute() or ".." in path.parts or member.issym() or member.islnk() or member.isdev():
            raise SystemExit(f"unsafe release archive entry: {member.name}")
    bundle.extractall(target, members=members)
PY
}

write_status() {
  state=$1
  request_id=$2
  target_version=$3
  message=$4
  requested_at=$5
  started_at=$6
  finished_at=$7
  heartbeat=$(timestamp)
  python3 - "$status_file" "$state" "$request_id" "$target_version" "$message" "$requested_at" "$started_at" "$finished_at" "$heartbeat" <<'PY'
import json
import os
import sys
from pathlib import Path

path = Path(sys.argv[1])
path.parent.mkdir(parents=True, exist_ok=True)
payload = {
    "agentConfigured": True,
    "agentReady": True,
    "state": sys.argv[2],
    "requestId": sys.argv[3],
    "targetVersion": sys.argv[4],
    "message": sys.argv[5][-1200:],
    "requestedAt": sys.argv[6],
    "startedAt": sys.argv[7],
    "finishedAt": sys.argv[8],
    "heartbeatAt": sys.argv[9],
}
temporary = path.with_name("." + path.name + ".tmp")
with temporary.open("w", encoding="utf-8") as stream:
    json.dump(payload, stream, ensure_ascii=False, separators=(",", ":"))
    stream.write("\n")
os.chmod(temporary, 0o644)
os.replace(temporary, path)
PY
}

request_summary() {
  python3 - "$request_file" "$allowed_repository" <<'PY'
import json
import re
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path
from urllib.parse import urlparse

path = Path(sys.argv[1])
allowed_repository = sys.argv[2].strip()
if not path.exists():
    raise SystemExit(10)
data = json.loads(path.read_text(encoding="utf-8"))
required = ("requestId", "repository", "targetVersion", "releaseTag", "assetName", "assetUrl", "requestedAt")
if any(not isinstance(data.get(key), str) or not data[key].strip() for key in required):
    raise SystemExit("invalid update request fields")
repository = data["repository"].strip()
if not re.fullmatch(r"[A-Za-z0-9._-]+/[A-Za-z0-9._-]+", repository):
    raise SystemExit("invalid update repository")
if allowed_repository and repository != allowed_repository:
    raise SystemExit("update repository does not match host policy")
if not re.fullmatch(r"[A-Za-z0-9._-]{1,96}", data["requestId"]):
    raise SystemExit("invalid update request id")
if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", data["targetVersion"]):
    raise SystemExit("invalid update version")
if not re.fullmatch(r"v?[0-9]+\.[0-9]+\.[0-9]+", data["releaseTag"]):
    raise SystemExit("invalid release tag")
if data["releaseTag"].removeprefix("v") != data["targetVersion"]:
    raise SystemExit("release tag and target version do not match")
allowed_assets = {
    f"erdai-agent-stable-{data['releaseTag']}.tar.gz",
    f"erdai-agent-stable-{data['targetVersion']}.tar.gz",
    f"erdai-agent-{data['releaseTag']}.tar.gz",
    f"erdai-agent-{data['targetVersion']}.tar.gz",
}
if data["assetName"] not in allowed_assets:
    raise SystemExit("invalid release asset name")
parsed = urlparse(data["assetUrl"])
expected_path = f"/{repository}/releases/download/"
if parsed.scheme != "https" or parsed.netloc.lower() != "github.com" or not parsed.path.startswith(expected_path):
    raise SystemExit("release asset URL is not a GitHub download URL")
size = data.get("assetSize", 0)
if not isinstance(size, int) or size < 0 or size > 4 * 1024 * 1024 * 1024:
    raise SystemExit("invalid release asset size")
digest = data.get("assetDigest", "")
if not re.fullmatch(r"sha256:[0-9a-fA-F]{64}", digest):
    raise SystemExit("invalid release asset digest")
try:
    requested_at = datetime.fromisoformat(data["requestedAt"].replace("Z", "+00:00"))
except ValueError as error:
    raise SystemExit("invalid update request timestamp") from error
if requested_at.tzinfo is None:
    raise SystemExit("update request timestamp must include a timezone")
now = datetime.now(timezone.utc)
if requested_at < now - timedelta(minutes=15) or requested_at > now + timedelta(minutes=2):
    raise SystemExit("update request has expired")
print("|".join(str(data.get(key, "")) for key in ("requestId", "repository", "targetVersion", "releaseTag", "assetName", "assetUrl", "assetDigest", "assetSize", "requestedAt")))
PY
}

last_status() {
  python3 - "$status_file" <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
if not path.exists():
    print("|")
    raise SystemExit(0)
try:
    data = json.loads(path.read_text(encoding="utf-8"))
except (OSError, ValueError):
    print("|")
    raise SystemExit(0)
print(f"{data.get('requestId', '')}|{data.get('state', '')}")
PY
}

refresh_status() {
  python3 - "$status_file" <<'PY'
import json
import os
import sys
from datetime import datetime, timezone
from pathlib import Path

path = Path(sys.argv[1])
if not path.exists():
    raise SystemExit(0)
try:
    payload = json.loads(path.read_text(encoding="utf-8"))
except (OSError, ValueError):
    raise SystemExit(0)
payload["agentConfigured"] = True
payload["agentReady"] = True
payload["heartbeatAt"] = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
temporary = path.with_name("." + path.name + ".tmp")
with temporary.open("w", encoding="utf-8") as stream:
    json.dump(payload, stream, ensure_ascii=False, separators=(",", ":"))
    stream.write("\n")
os.chmod(temporary, 0o644)
os.replace(temporary, path)
PY
}

refresh_terminal_or_idle() {
  current=$(last_status)
  IFS='|' read -r current_request current_state <<EOF
$current
EOF
  case "$current_state" in
    succeeded|failed) refresh_status ;;
    *) write_status idle "" "" "waiting for Stable update request" "" "" "" ;;
  esac
}

reload_if_updated() {
  installed_script=$root/app/scripts/erdai-update-agent.sh
  test -x "$installed_script" || return 0
  installed_digest=$(sha256sum "$installed_script" | awk '{print $1}')
  if [ "$installed_digest" != "$running_script_digest" ]; then
    exec "$installed_script"
  fi
}

clear_request() {
  expected_request_id=$1
  python3 - "$request_file" "$expected_request_id" <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
expected = sys.argv[2]
try:
    data = json.loads(path.read_text(encoding="utf-8"))
except (OSError, ValueError):
    raise SystemExit(0)
if data.get("requestId") == expected:
    path.unlink(missing_ok=True)
PY
}

download_and_apply() {
  request_id=$1
  target_version=$2
  asset_name=$3
  asset_url=$4
  asset_digest=$5
  asset_size=$6
  work_dir=$work_root/$request_id
  archive=$work_dir/$asset_name
  rm -rf "$work_dir"
  install -d -m 700 "$work_dir"
  available_kb=$(df -Pk "$work_root" | awk 'NR == 2 {print $4}')
  required_kb=$((asset_size / 1024 * 3 + 524288))
  [ "$available_kb" -ge "$required_kb" ] || { echo "insufficient disk space for Stable release" >&2; return 1; }
  curl --proto '=https' --proto-redir '=https' --tlsv1.2 --fail --location --silent --show-error \
    --connect-timeout 10 --retry 3 --retry-all-errors --max-time 1800 \
    --output "$archive" "$asset_url"
  actual_size=$(wc -c < "$archive" | tr -d ' ')
  if [ "$asset_size" -gt 0 ] && [ "$actual_size" -ne "$asset_size" ]; then
    echo "release asset size mismatch" >&2
    return 1
  fi
  expected_digest=${asset_digest#sha256:}
  actual_digest=$(sha256sum "$archive" | awk '{print $1}')
  [ "$actual_digest" = "$expected_digest" ] || { echo "release asset digest mismatch" >&2; return 1; }
  extract_dir=$work_dir/extracted
  install -d -m 700 "$extract_dir"
  safe_extract_archive "$archive" "$extract_dir"
  bundle=$(find "$extract_dir" -mindepth 2 -maxdepth 3 -type f -name manifest.env -exec dirname {} \; | head -n 1)
  [ -n "$bundle" ] || { echo "release bundle manifest is missing" >&2; return 1; }
  for file in manifest.env SHA256SUMS images.tar app.tar.gz; do
    test -f "$bundle/$file" || { echo "release bundle file is missing: $file" >&2; return 1; }
  done
  release_app=$work_dir/release-app
  install -d -m 700 "$release_app"
  safe_extract_archive "$bundle/app.tar.gz" "$release_app"
  release_script=$release_app/scripts/deploy-250.sh
  test -x "$release_script" || { echo "release deploy script is missing" >&2; return 1; }
  "$release_script" "$bundle"
}

process_request() {
  summary=$1
  IFS='|' read -r request_id repository target_version release_tag asset_name asset_url asset_digest asset_size requested_at <<EOF
$summary
EOF
  started_at=$(timestamp)
  write_status running "$request_id" "$target_version" "downloading Stable $target_version" "$requested_at" "$started_at" ""
  (
    while :; do
      sleep 15
      refresh_status
    done
  ) &
  heartbeat_pid=$!
  if message=$(download_and_apply "$request_id" "$target_version" "$asset_name" "$asset_url" "$asset_digest" "$asset_size" "$requested_at" 2>&1); then
    kill "$heartbeat_pid" >/dev/null 2>&1 || true
    wait "$heartbeat_pid" 2>/dev/null || true
    write_status succeeded "$request_id" "$target_version" "Stable $target_version upgrade completed" "$requested_at" "$started_at" "$(timestamp)"
    clear_request "$request_id"
    rm -rf "$work_root/$request_id"
    reload_if_updated
  else
    kill "$heartbeat_pid" >/dev/null 2>&1 || true
    wait "$heartbeat_pid" 2>/dev/null || true
    printf 'Stable %s upgrade failed: %s\n' "$target_version" "$message" >&2
    write_status failed "$request_id" "$target_version" "Stable upgrade failed; inspect erdai-update-agent service logs" "$requested_at" "$started_at" "$(timestamp)"
    clear_request "$request_id"
  fi
}

case "${1:-run}" in
  run) ;;
  --check-request) request_summary; exit 0 ;;
  --write-idle) write_status idle "" "" "waiting for Stable update request" "" "" ""; exit 0 ;;
  *) echo "usage: erdai-update-agent.sh [--check-request|--write-idle]" >&2; exit 2 ;;
esac

while :; do
  request_error=/tmp/erdai-update-agent-request-error
  request_fingerprint=$(sha256sum "$request_file" 2>/dev/null | awk '{print $1}' || true)
  if summary=$(request_summary 2>"$request_error"); then
    current=$(last_status)
    IFS='|' read -r current_request current_state <<EOF
$current
EOF
    IFS='|' read -r request_id repository target_version release_tag asset_name asset_url asset_digest asset_size requested_at <<EOF
$summary
EOF
    if [ "$current_request" != "$request_id" ] || [ "$current_state" = "pending" ] || [ "$current_state" = "running" ] || [ -z "$current_state" ]; then
      process_request "$summary"
    else
      refresh_status
    fi
  else
    request_exit=$?
    if [ "$request_exit" -eq 10 ]; then
      refresh_terminal_or_idle
    else
      error=$(tr '\n' ' ' < "$request_error")
      write_status failed "" "" "$error" "" "" "$(timestamp)"
      current_fingerprint=$(sha256sum "$request_file" 2>/dev/null | awk '{print $1}' || true)
      if [ -n "$request_fingerprint" ] && [ "$current_fingerprint" = "$request_fingerprint" ]; then
        rm -f "$request_file"
      fi
    fi
  fi
  sleep "$poll_seconds"
done
