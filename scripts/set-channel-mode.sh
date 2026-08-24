#!/bin/sh
set -eu

mode=${1:?usage: set-channel-mode.sh off|shadow|active}
case "$mode" in
	off|shadow|active) ;;
	*) echo "invalid channel mode: $mode" >&2; exit 2 ;;
esac

root=${ERDAI_INSTALL_ROOT:-/opt/erdai-agent}
env_file=$root/.env
admin_token=$(sed -n 's/^ERDAI_ADMIN_TOKEN=//p' "$env_file" | tail -n 1)
test "${#admin_token}" -ge 32

output=$(mktemp /tmp/erdai-channel-mode.XXXXXX)
trap 'rm -f "$output"' EXIT INT TERM

curl -fsS -o "$output" -X PUT \
	http://127.0.0.1:6282/api/v1/integrations/channel_runtime \
	-H 'content-type: application/json' \
	-H "X-Erdai-Admin-Token: $admin_token" \
	--data "{\"mode\":\"$mode\",\"captureUnaddressedGroups\":true,\"deliveryPollSeconds\":1}"

python3 - "$output" "$mode" <<'PY'
import json
import sys

config = json.load(open(sys.argv[1], encoding="utf-8"))["data"]["config"]
assert config["mode"] == sys.argv[2], config
print(f"channel_mode={config['mode']}")
PY
