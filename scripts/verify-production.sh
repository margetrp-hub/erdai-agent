#!/bin/sh
set -eu

root=/opt/erdai-agent
env_file=$root/.env
compose_file=$root/app/compose.production.yml
expected_mode=${1:?usage: verify-production.sh MODE IMAGE SCHEMA MEMORY_TOTAL_BYTES}
expected_image=${2:?usage: verify-production.sh MODE IMAGE SCHEMA MEMORY_TOTAL_BYTES}
expected_schema=${3:?usage: verify-production.sh MODE IMAGE SCHEMA MEMORY_TOTAL_BYTES}
expected_memory_total=${4:?usage: verify-production.sh MODE IMAGE SCHEMA MEMORY_TOTAL_BYTES}
expected_browser_image=${ERDAI_MONITOR_BROWSER_IMAGE:?ERDAI_MONITOR_BROWSER_IMAGE is required}

case "$expected_mode" in off|shadow|active) ;; *) echo "invalid mode" >&2; exit 2;; esac
case "$expected_schema:$expected_memory_total" in *[!0-9:]*|:*) echo "invalid numeric expectation" >&2; exit 2;; esac

curl -fsS http://127.0.0.1:6280/healthz >/dev/null
curl -fsS http://127.0.0.1:6282/healthz >/dev/null
index_html=$(curl -fsS http://127.0.0.1:6282/)
web_asset=$(printf '%s' "$index_html" | sed -n 's/.*<script[^>]*src="\([^"]*\.js\)"[^>]*>.*/\1/p' | head -n 1)
if [ -n "$web_asset" ]; then
  case "$web_asset" in /*) ;; *) web_asset=/$web_asset;; esac
else
  web_asset=/app.js
fi
web_js=$(curl -fsS "http://127.0.0.1:6282$web_asset")
for marker in /api/v1/runtime/media-quotas /api/v1/mcp/servers /api/v1/skills /api/v1/integrations/content_boundary_policy /api/v1/agent-instances /api/v1/credentials /api/v1/update/status /api/v1/update/request '/api/v1/usage/stats?hours=24'; do
  case "$web_js" in
    *"$marker"*) ;;
    *) echo "WebUI marker missing: $marker" >&2; exit 1 ;;
  esac
done

runtime_token=$(sed -n 's/^ERDAI_RUNTIME_TOKEN=//p' "$env_file" | tail -n 1)
admin_token=$(sed -n 's/^ERDAI_ADMIN_TOKEN=//p' "$env_file" | tail -n 1)
test "${#runtime_token}" -ge 32
test "${#admin_token}" -ge 32
stamp=$(date -u +%Y%m%d-%H%M%S)
credential_check_name=ERDAI_VERIFY_TOKEN
credential_check_value=erdai-release-credential-check-$stamp-$$

prepare_output=$(mktemp /tmp/erdai-prepare.XXXXXX)
anonymous_output=$(mktemp /tmp/erdai-anonymous.XXXXXX)
create_output=$(mktemp /tmp/erdai-create.XXXXXX)
read_output=$(mktemp /tmp/erdai-read.XXXXXX)
delete_output=$(mktemp /tmp/erdai-delete.XXXXXX)
channel_config_output=$(mktemp /tmp/erdai-channel-config.XXXXXX)
platform_status_output=$(mktemp /tmp/erdai-platform-status.XXXXXX)
credentials_output=$(mktemp /tmp/erdai-credentials.XXXXXX)
credential_put_output=$(mktemp /tmp/erdai-credential-put.XXXXXX)
cleanup_outputs() {
  rm -f "$prepare_output" "$anonymous_output" "$create_output" "$read_output" \
    "$delete_output" "$channel_config_output" "$platform_status_output" \
    "$credentials_output" "$credential_put_output"
}
cleanup_credential_check() {
  curl -sS -o /dev/null -X DELETE \
    -H "X-Erdai-Admin-Token: $admin_token" \
    "http://127.0.0.1:6282/api/v1/credentials/$credential_check_name" || true
}
trap 'cleanup_credential_check; cleanup_outputs' EXIT INT TERM

channel_config_status=$(curl -sS -o "$channel_config_output" -w '%{http_code}' \
  -H "X-Erdai-Admin-Token: $admin_token" \
  http://127.0.0.1:6282/api/v1/integrations/channel_runtime)
test "$channel_config_status" = 200
python3 - "$channel_config_output" "$expected_mode" <<'PY'
import json
import sys

channel = json.load(open(sys.argv[1], encoding="utf-8"))["data"]["config"]
assert channel["mode"] == sys.argv[2]
assert set(channel) == {"mode", "captureUnaddressedGroups", "deliveryPollSeconds"}
PY

credentials_status=$(curl -sS -o "$credentials_output" -w '%{http_code}' \
  -H "X-Erdai-Admin-Token: $admin_token" \
  http://127.0.0.1:6282/api/v1/credentials)
test "$credentials_status" = 200
test -z "$(grep -F "$runtime_token" "$credentials_output" || true)"
credential_put_status=$(curl -sS -o "$credential_put_output" -w '%{http_code}' \
  -X PUT http://127.0.0.1:6282/api/v1/credentials/$credential_check_name \
  -H 'content-type: application/json' \
  -H "X-Erdai-Admin-Token: $admin_token" \
  --data "{\"value\":\"$credential_check_value\"}")
test "$credential_put_status" = 200
grep -q '"persisted":true' "$credential_put_output"
test -z "$(grep -F "$credential_check_value" "$credential_put_output" || true)"
credentials_status=$(curl -sS -o "$credentials_output" -w '%{http_code}' \
  -H "X-Erdai-Admin-Token: $admin_token" \
  http://127.0.0.1:6282/api/v1/credentials)
test "$credentials_status" = 200
test -z "$(grep -F "$credential_check_value" "$credentials_output" || true)"
python3 - "$credentials_output" "$credential_check_name" <<'PY'
import json
import sys

items = json.load(open(sys.argv[1], encoding="utf-8"))["data"]["items"]
item = next(item for item in items if item["name"] == sys.argv[2])
assert item["configured"] is True
assert item["persisted"] is True
PY
credential_delete_status=$(curl -sS -o /dev/null -w '%{http_code}' \
  -X DELETE http://127.0.0.1:6282/api/v1/credentials/$credential_check_name \
  -H "X-Erdai-Admin-Token: $admin_token")
test "$credential_delete_status" = 200
credentials_status=$(curl -sS -o "$credentials_output" -w '%{http_code}' \
  -H "X-Erdai-Admin-Token: $admin_token" \
  http://127.0.0.1:6282/api/v1/credentials)
test "$credentials_status" = 200
test -z "$(grep -F "$credential_check_name" "$credentials_output" | grep -F '"configured":true' || true)"

prepare_payload='{"transport":"verification","conversationRef":"release-check","senderRef":"release-check","message":"ping","hasImage":false,"hasAudio":false,"isAdmin":true,"legacyModel":""}'
runtime_prepare_status=$(curl -sS -o "$prepare_output" -w '%{http_code}' \
  -X POST http://127.0.0.1:6280/api/v1/runtime/prepare \
  -H 'content-type: application/json' \
  -H "Authorization: Bearer $runtime_token" \
  --data "$prepare_payload")
test "$runtime_prepare_status" = 200
grep -q '"messagePolicy"' "$prepare_output"

anonymous_status=$(curl -sS -o "$anonymous_output" -w '%{http_code}' \
  -X PUT http://127.0.0.1:6280/api/v1/runtime/config \
  -H 'content-type: application/json' --data '{}')
test "$anonymous_status" = 404

write_check_id=release-write-check-$stamp-$$
cleanup_write_check() {
  curl -sS -o /dev/null -X DELETE \
    -H "X-Erdai-Admin-Token: $admin_token" \
    "http://127.0.0.1:6282/api/v1/tools/$write_check_id" || true
}
trap 'cleanup_write_check; cleanup_credential_check; cleanup_outputs' EXIT INT TERM
created_status=$(curl -sS -o "$create_output" -w '%{http_code}' \
  -X POST http://127.0.0.1:6282/api/v1/tools \
  -H 'content-type: application/json' \
  -H "X-Erdai-Admin-Token: $admin_token" \
  --data "{\"id\":\"$write_check_id\",\"name\":\"$write_check_id\",\"description\":\"temporary verification\",\"capabilities\":[],\"riskLevel\":0,\"enabled\":false,\"adapterRef\":\"verification\",\"allowedAuthorities\":[\"admin\"],\"approvalMode\":\"admin_only\",\"timeoutSeconds\":5,\"inputSchema\":{\"type\":\"object\",\"properties\":{}}}")
test "$created_status" = 201
read_status=$(curl -sS -o "$read_output" -w '%{http_code}' \
  -H "X-Erdai-Admin-Token: $admin_token" \
  "http://127.0.0.1:6282/api/v1/tools/$write_check_id")
test "$read_status" = 200
deleted_status=$(curl -sS -o "$delete_output" -w '%{http_code}' \
  -X DELETE -H "X-Erdai-Admin-Token: $admin_token" \
  "http://127.0.0.1:6282/api/v1/tools/$write_check_id")
test "$deleted_status" = 200
trap cleanup_outputs EXIT INT TERM

test -z "$(docker port erdai-agent 6185/tcp 2>/dev/null || true)"
test "$(docker inspect -f '{{.Config.Image}}' erdai-agent)" = "$expected_image"
test "$(docker inspect -f '{{.Config.User}}' erdai-agent)" = "1000:1000"
test "$(docker inspect -f '{{.HostConfig.ReadonlyRootfs}}' erdai-agent)" = "true"

test "$(docker inspect -f '{{.State.Health.Status}}' erdai-agent)" = "healthy"
test "$(docker inspect -f '{{.State.OOMKilled}}' erdai-agent)" = "false"
test "$(docker inspect -f '{{.RestartCount}}' erdai-agent)" = "0"
test "$(docker inspect -f '{{.Config.Image}}' erdai-monitor-browser)" = "$expected_browser_image"
test "$(docker inspect -f '{{.State.Health.Status}}' erdai-monitor-browser)" = "healthy"
test "$(docker inspect -f '{{.State.OOMKilled}}' erdai-monitor-browser)" = "false"
test "$(docker inspect -f '{{.RestartCount}}' erdai-monitor-browser)" = "0"
test "$(docker inspect -f '{{.HostConfig.ReadonlyRootfs}}' erdai-monitor-browser)" = "true"
test -z "$(docker port erdai-monitor-browser 9222/tcp 2>/dev/null || true)"

test "$(docker inspect -f '{{json .Config.Entrypoint}}' erdai-agent)" = '["/app/erdai-agent"]'
test "$(docker top erdai-agent -eo pid,args | tail -n +2 | wc -l | tr -d ' ')" = 1
docker top erdai-agent -eo pid,args | tail -n +2 | \
  grep -Eq '^[[:space:]]*[0-9]+[[:space:]]+/app/erdai-agent[[:space:]]*$'
core_memory=$(docker inspect -f '{{.HostConfig.Memory}}' erdai-agent)
[ "$core_memory" -le 536870912 ]
if docker container inspect erdai-embedding >/dev/null 2>&1; then
  test "$(docker inspect -f '{{.State.Health.Status}}' erdai-embedding)" = "healthy"
  embedding_memory=$(docker inspect -f '{{.HostConfig.Memory}}' erdai-embedding)
  browser_memory=$(docker inspect -f '{{.HostConfig.Memory}}' erdai-monitor-browser)
  [ $((core_memory + embedding_memory + browser_memory)) -le "$expected_memory_total" ]
fi
test "$(docker inspect -f '{{.HostConfig.MemorySwap}}' erdai-agent)" -le "805306368"
test "$(docker inspect -f '{{.HostConfig.NanoCpus}}' erdai-agent)" -le "1500000000"
test "$(docker inspect -f '{{.HostConfig.PidsLimit}}' erdai-agent)" = "128"
docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' erdai-agent | grep -qx 'GOMAXPROCS=1'
docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' erdai-agent | grep -qx 'GOMEMLIMIT=384MiB'
docker inspect erdai-agent | python3 -c \
  'import json,sys; assert json.load(sys.stdin)[0]["Config"]["Healthcheck"]["StartPeriod"] == 30000000000'
docker inspect -f '{{json .Config.Healthcheck.Test}}' erdai-agent | \
  grep -q '/app/erdai-agent'
test "$(docker inspect -f '{{ index .HostConfig.LogConfig.Config "max-size" }}' erdai-agent)" = "5m"
test "$(docker inspect -f '{{ index .HostConfig.LogConfig.Config "max-file" }}' erdai-agent)" = "3"

for retired in erdai-agent-core erdai-agent-runtime astrbot doubao-agent-core erdai-agent-web; do
  test -z "$(docker ps -q --filter name=^/$retired$)"
done

python3 - "$root/data/erdai-agent-core.sqlite3" "$expected_schema" <<'PY'
import json
import sqlite3
import sys

with sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True) as database:
    assert database.execute("PRAGMA quick_check").fetchone()[0] == "ok"
    assert database.execute("PRAGMA user_version").fetchone()[0] == int(sys.argv[2])
    assert database.execute(
        "SELECT count(*) FROM runtime_database_migrations WHERE id = 'runtime-database-v1'"
    ).fetchone()[0] == 1
    for table in (
        "agent_runs", "agent_deliveries", "platform_reply_routes",
        "platform_sent_deliveries", "agent_memories", "conversation_events",
        "conversation_state", "relationship_events", "member_relationship_state",
        "media_quota_config", "media_quota_usage", "agent_policy_templates",
        "agent_instances", "agent_instance_connectors", "agent_instance_routes",
        "agent_search_entities", "agent_recent_attachments",
    ):
        database.execute(f"SELECT count(*) FROM {table}").fetchone()
    assert database.execute(
        "SELECT count(*) FROM model_endpoints "
        "WHERE adapter_ref IN ('grok_generate_image', 'generate_image') AND enabled = 1"
    ).fetchone()[0] >= 1
    for tool in ("grok_generate_image", "grok_generate_video"):
        assert database.execute(
            "SELECT count(*) FROM tools WHERE name = ? AND enabled = 1", (tool,)
        ).fetchone()[0] == 1
    for tool in ("read_document", "create_office_document"):
        assert database.execute(
            "SELECT count(*) FROM tools WHERE name = ? AND enabled = 1", (tool,)
        ).fetchone()[0] == 1
    for skill in ("office-read", "office-create", "image-understanding", "web-research"):
        assert database.execute(
            "SELECT count(*) FROM skills WHERE id = ? AND enabled = 1", (skill,)
        ).fetchone()[0] == 1
    persona = database.execute(
        "SELECT character_version, source_version, visual_description FROM personas WHERE id = 'doubao'"
    ).fetchone()
    assert persona and persona[0] and persona[1], persona[:2] if persona else None
    assert persona[2] and len(persona[2]) >= 80
    assert "明确成年" in persona[2]
    assert any(marker in persona[2] for marker in ("现实世界", "真人", "写实", "真实"))
    assert any(marker in persona[2] for marker in ("自然", "真实", "清透"))
    assert database.execute(
        "SELECT count(*) FROM persona_samples WHERE persona_id = 'doubao' AND enabled = 1"
    ).fetchone()[0] >= 40
    boundary = json.loads(database.execute(
        "SELECT config_json FROM integration_settings WHERE id = 'content_boundary_policy'"
    ).fetchone()[0])
    assert boundary["enabled"] is True
    assert boundary["sexualAction"] == "refuse"
    assert boundary["violenceAction"] == "refuse"
    assert boundary["abuseAction"] == "counter"
    assert boundary["provocationAction"] == "model"
    for field in ("sexualReplies", "violenceReplies", "abuseReplies", "provocationReplies"):
        assert boundary[field]
        assert all(0 < len(reply) <= 20 for reply in boundary[field]), (field, boundary[field])
    for sample in (
        "doubao-sample-malicious-provocation",
        "doubao-sample-obscene-boundary",
        "doubao-sample-threat-boundary",
    ):
        assert database.execute(
            "SELECT count(*) FROM persona_samples WHERE id = ? AND enabled = 1", (sample,)
        ).fetchone()[0] == 1
    assert "本句独有的细节" in database.execute(
        "SELECT reply_style FROM runtime_config WHERE id = 1"
    ).fetchone()[0]
    for server in ("context7-docs", "microsoft-learn", "cloudflare-docs"):
        assert database.execute(
            "SELECT count(*) FROM mcp_servers WHERE id = ? AND enabled = 1", (server,)
        ).fetchone()[0] == 1
PY

platform_status_code=$(curl -sS -o "$platform_status_output" -w '%{http_code}' \
  -H "X-Erdai-Admin-Token: $admin_token" \
  http://127.0.0.1:6282/api/v1/platforms/runtime-status)
test "$platform_status_code" = 200
python3 - "$platform_status_output" <<'PY'
import json
import sys

items = json.load(open(sys.argv[1], encoding="utf-8"))["data"]
qq = next(item for item in items if item["type"] == "qq_official")
assert qq["status"] in {"connecting", "connected"}, qq
details = qq.get("details") or {}
assert details.get("groupMessagesIntent") is True, details
assert int(details.get("requestedIntents", 0)) > 0, details
visible = {
    key: details[key]
    for key in (
        "requestedIntents", "groupMessagesIntent", "lastRawInboundKind",
        "lastRawInboundAt", "rawInboundCount", "parseFailureCount",
        "lastParseFailureAt",
    )
    if key in details
}
print("qq_connector=" + json.dumps({"status": qq["status"], "details": visible}, ensure_ascii=False))
PY

export ERDAI_RELEASE_IMAGE="$expected_image"
export ERDAI_MONITOR_BROWSER_IMAGE="$expected_browser_image"
docker compose --env-file "$env_file" -f "$compose_file" config -q
services=$(docker compose --env-file "$env_file" -f "$compose_file" config --services)
test "$(printf '%s\n' "$services" | wc -l)" -eq 3
printf '%s\n' "$services" | grep -qx erdai-agent
printf '%s\n' "$services" | grep -qx erdai-embedding
printf '%s\n' "$services" | grep -qx erdai-monitor-browser
test "$(stat -c '%a %U:%G' "$root/app")" = "755 root:root"
test -z "$(find "$root/app" -xdev -perm /022 -print -quit)"

printf 'anonymous_write=%s\n' "$anonymous_status"
printf 'runtime_prepare=%s\n' "$runtime_prepare_status"
printf 'admin_create=%s\n' "$created_status"
printf 'admin_read=%s\n' "$read_status"
printf 'admin_delete=%s\n' "$deleted_status"
stat -c '%a %U:%G %n' "$env_file" "$root/runtime.env" "$root/app" "$root/data"
cat "$channel_config_output"
printf '\n'
printf 'channel_mode=%s\n' "$expected_mode"
docker compose --env-file "$env_file" -f "$compose_file" ps
