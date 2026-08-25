#!/bin/sh
set -eu

package_dir=${1:?usage: deploy-250.sh RELEASE_BUNDLE [--dry-run]}
mode=${2:-apply}
case "$mode" in apply|--dry-run) ;; *) echo "invalid mode" >&2; exit 2;; esac

root=/opt/erdai-agent
env_file=$root/.env
stamp=$(date -u +%Y%m%d-%H%M%S)
stage=
rollback_dir=
old_container=
old_image_ref=
old_image_id=
old_channel_mode=off
channel_quiesced=0
swapped_app=0
rollback_armed=0

fail() { echo "$*" >&2; exit 1; }
manifest_value() {
  key=$1
  value=$(sed -n "s/^${key}=//p" "$package_dir/manifest.env")
  [ "$(printf '%s\n' "$value" | wc -l | tr -d ' ')" = 1 ] || fail "invalid manifest field: $key"
  [ -n "$value" ] || fail "missing manifest field: $key"
  printf '%s' "$value"
}
safe_value() {
  case "$1" in *[!A-Za-z0-9._:/@-]*|'') fail "unsafe manifest value";; esac
}
persist_env_value() {
  key=$1
  value=$2
  if grep -q "^${key}=" "$env_file"; then
    sed -i "s|^${key}=.*|${key}=${value}|" "$env_file"
  else
    printf '%s=%s\n' "$key" "$value" >> "$env_file"
  fi
}
cleanup() {
  status=$?
  trap - EXIT INT TERM
  if [ "$status" -ne 0 ] && [ "$rollback_armed" -eq 1 ]; then
    echo "release failed; restoring the temporary rollback point" >&2
    set +e
    docker rm -f erdai-agent >/dev/null 2>&1 || true
    [ -z "$old_image_ref" ] || docker image tag "$old_image_id" "$old_image_ref" >/dev/null 2>&1 || true
    for database in erdai-agent-core.sqlite3 erdai-runtime.sqlite3; do
      if [ -n "$rollback_dir" ] && [ -f "$rollback_dir/$database" ]; then
        cp -f "$rollback_dir/$database" "$root/data/$database"
        rm -f "$root/data/$database-wal" "$root/data/$database-shm"
        chown 1000:1000 "$root/data/$database"
      fi
    done
    if [ "$swapped_app" -eq 1 ] && [ -d "$rollback_dir/app" ]; then
      rm -rf "$root/app"
      mv "$rollback_dir/app" "$root/app"
    fi
    if [ -f "$rollback_dir/.env" ]; then
      cp -a "$rollback_dir/.env" "$env_file"
    fi
    if [ -n "$old_container" ] && docker container inspect "$old_container" >/dev/null 2>&1; then
      docker rename "$old_container" erdai-agent >/dev/null 2>&1 && docker start erdai-agent >/dev/null 2>&1 || true
    elif [ -n "$old_image_ref" ] && [ -f "$root/app/compose.production.yml" ]; then
      ERDAI_RELEASE_IMAGE="$old_image_ref" ERDAI_EMBEDDING_IMAGE="$embedding_image" \
        docker compose --env-file "$env_file" -f "$root/app/compose.production.yml" \
        up -d --no-build --force-recreate erdai-agent >/dev/null 2>&1 || true
    fi
    attempt=0
    while docker container inspect erdai-agent >/dev/null 2>&1 && [ "$attempt" -lt 60 ]; do
      if curl -fsS http://127.0.0.1:6280/healthz >/dev/null 2>&1 &&
        [ "$(docker inspect -f '{{.State.Health.Status}}' erdai-agent)" = healthy ]; then
        ERDAI_INSTALL_ROOT="$root" "$root/app/scripts/set-channel-mode.sh" "$old_channel_mode" >/dev/null 2>&1 || true
        break
      fi
      attempt=$((attempt + 1))
      sleep 1
    done
  elif [ "$status" -ne 0 ] && [ "$channel_quiesced" -eq 1 ]; then
    set +e
    if docker ps --format '{{.Names}}' | grep -qx erdai-agent; then
      ERDAI_INSTALL_ROOT="$root" "$root/app/scripts/set-channel-mode.sh" "$old_channel_mode" >/dev/null 2>&1 || true
    fi
  fi
  [ -z "$stage" ] || rm -rf "$stage"
  if [ "$status" -eq 0 ]; then [ -z "$rollback_dir" ] || rm -rf "$rollback_dir"; fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

[ "$(id -u)" -eq 0 ] || fail "run as root"
for command in docker flock python3 sha256sum curl; do command -v "$command" >/dev/null || fail "$command is required"; done
test -d "$package_dir"
for file in manifest.env SHA256SUMS images.tar app.tar.gz; do test -f "$package_dir/$file" || fail "missing $file"; done
(cd "$package_dir" && sha256sum -c SHA256SUMS)

release=$(manifest_value RELEASE_ID)
release_image=$(manifest_value RELEASE_IMAGE)
embedding_image=$(manifest_value EMBEDDING_IMAGE)
schema=$(manifest_value SCHEMA_VERSION)
platform=$(manifest_value PLATFORM)
source_revision=$(manifest_value SOURCE_REVISION)
memory_total=$(manifest_value MEMORY_LIMIT_TOTAL_BYTES)
for value in "$release" "$release_image" "$embedding_image" "$platform" "$source_revision"; do safe_value "$value"; done
case "$schema:$memory_total" in *[!0-9:]*|:*) fail "invalid numeric manifest field";; esac
[ "$platform" = linux/amd64 ] || fail "only linux/amd64 release bundles are accepted"
[ "$memory_total" -le 1073741824 ] || fail "release memory budget exceeds the 1.6-GiB VPS safety limit"

install -d -m 700 "$root"
exec 9>"$root/.erdai-agent-release.lock"
flock -n 9 || fail "another ErDai release is already running"
docker info >/dev/null
[ "$(docker version --format '{{.Server.Os}}/{{.Server.Arch}}')" = "$platform" ] || fail "Docker architecture mismatch"
test -f "$env_file" || fail "missing $env_file"
test -d "$root/data" || fail "missing $root/data"
test -r "$root/models/bge-small-zh-v1.5-q4_k_m.gguf" || fail "embedding model is missing"
docker container inspect erdai-embedding >/dev/null 2>&1 || fail "embedding container must already exist"
docker network inspect erdai-agent-internal >/dev/null
docker network inspect grok-egress >/dev/null
[ "$(awk '/MemAvailable:/ {print $2}' /proc/meminfo)" -ge 393216 ] || fail "less than 384 MiB RAM is available"
[ "$(df -Pk "$root" | awk 'NR == 2 {print $4}')" -ge 524288 ] || fail "less than 512 MiB disk is available"

stage=$root/.release-$release-$stamp
rollback_dir=$root/.rollback-$release-$stamp
install -d -m 700 "$stage" "$rollback_dir"
tar -xzf "$package_dir/app.tar.gz" -C "$stage"
test -f "$stage/compose.production.yml"
test -x "$stage/scripts/verify-production.sh"
chmod 755 "$stage"
chown root:root "$stage"
ERDAI_RELEASE_IMAGE="$release_image" ERDAI_EMBEDDING_IMAGE="$embedding_image" docker compose --env-file "$env_file" -f "$stage/compose.production.yml" config -q

if [ "$mode" = --dry-run ]; then
  printf 'dry_run=ok\nrelease=%s\nimage=%s\nschema=%s\n' "$release" "$release_image" "$schema"
  exit 0
fi

docker load --input "$package_dir/images.tar" >/dev/null
core_image_id=$(docker image inspect -f '{{.Id}}' "$release_image")
[ "$(docker image inspect -f '{{ index .Config.Labels "org.opencontainers.image.version" }}' "$release_image")" = "$release" ] || fail "loaded Core version label does not match manifest"
[ "$(docker image inspect -f '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$release_image")" = "$source_revision" ] || fail "loaded Core revision label does not match manifest"
[ "$(docker image inspect -f '{{.Os}}/{{.Architecture}}' "$release_image")" = "$platform" ] || fail "loaded Core platform does not match manifest"
docker image inspect "$embedding_image" >/dev/null 2>&1 || fail "immutable embedding image is unavailable"
[ "$(docker inspect -f '{{.Config.Image}}' erdai-embedding)" = "$embedding_image" ] || fail "running embedding reference differs; upgrade it separately"
if docker container inspect erdai-agent >/dev/null 2>&1; then
  old_image_ref=$(docker container inspect -f '{{.Config.Image}}' erdai-agent)
  old_image_id=$(docker container inspect -f '{{.Image}}' erdai-agent)
fi
old_channel_mode=$(python3 - "$root/data/erdai-agent-core.sqlite3" <<'PY'
import json
import sqlite3
import sys

mode = "off"
try:
    with sqlite3.connect(f"file:{sys.argv[1]}?mode=ro", uri=True) as database:
        row = database.execute(
            "SELECT config_json FROM integration_settings WHERE id = 'channel_runtime'"
        ).fetchone()
        if row:
            mode = json.loads(row[0]).get("mode", "off")
except (OSError, sqlite3.Error, TypeError, ValueError):
    pass
print(mode if mode in {"off", "shadow", "active"} else "off")
PY
)
cp -a "$env_file" "$rollback_dir/.env"
if docker ps --format '{{.Names}}' | grep -qx erdai-agent; then
  channel_quiesced=1
  ERDAI_INSTALL_ROOT="$root" "$stage/scripts/set-channel-mode.sh" off
  sleep "${ERDAI_DRAIN_SECONDS:-15}"
fi

python3 - "$root/data/erdai-agent-core.sqlite3" "$rollback_dir/erdai-agent-core.sqlite3" "$root/data/erdai-runtime.sqlite3" "$rollback_dir/erdai-runtime.sqlite3" <<'PY'
import sqlite3
import sys
for source_path, target_path in zip(sys.argv[1::2], sys.argv[2::2]):
    try:
        with sqlite3.connect(f"file:{source_path}?mode=ro", uri=True) as source:
            with sqlite3.connect(target_path) as target:
                source.backup(target)
                assert target.execute("PRAGMA quick_check").fetchone()[0] == "ok"
    except sqlite3.OperationalError:
        pass
PY

rollback_armed=1
if docker container inspect erdai-agent >/dev/null 2>&1; then
  old_container=erdai-agent-rollback-$release-$stamp
  docker stop erdai-agent >/dev/null
  docker rename erdai-agent "$old_container"
fi
if [ -d "$root/app" ]; then mv "$root/app" "$rollback_dir/app"; fi
mv "$stage" "$root/app"
stage=
swapped_app=1

ERDAI_RELEASE_IMAGE="$release_image" ERDAI_EMBEDDING_IMAGE="$embedding_image" docker compose --env-file "$env_file" -f "$root/app/compose.production.yml" up -d --no-build --force-recreate erdai-agent
attempt=0
while [ "$attempt" -lt 180 ]; do
  if curl -fsS http://127.0.0.1:6280/healthz >/dev/null 2>&1 && curl -fsS http://127.0.0.1:6282/healthz >/dev/null 2>&1 && [ "$(docker inspect -f '{{.State.Health.Status}}' erdai-agent)" = healthy ]; then break; fi
  attempt=$((attempt + 1)); sleep 1
done
[ "$attempt" -lt 180 ] || { docker logs --tail 250 erdai-agent; fail "Core did not become healthy"; }

core_memory=$(docker inspect -f '{{.HostConfig.Memory}}' erdai-agent)
[ "$core_memory" -le 536870912 ] || fail "Core memory limit exceeds the release safety budget"
if docker container inspect erdai-embedding >/dev/null 2>&1; then
  [ "$(docker inspect -f '{{.State.Health.Status}}' erdai-embedding)" = healthy ] || fail "embedding is not healthy"
  embedding_memory=$(docker inspect -f '{{.HostConfig.Memory}}' erdai-embedding)
  [ $((core_memory + embedding_memory)) -le "$memory_total" ] || fail "container memory limits exceed the release budget"
fi
[ "$(docker inspect -f '{{.State.OOMKilled}}' erdai-agent)" = false ] || fail "Core was OOM-killed"
[ "$(docker inspect -f '{{.RestartCount}}' erdai-agent)" = 0 ] || fail "Core restarted during cutover"

ERDAI_RELEASE_IMAGE="$release_image" ERDAI_EMBEDDING_IMAGE="$embedding_image" "$root/app/scripts/verify-production.sh" off "$release_image" "$schema" "$memory_total"
ERDAI_INSTALL_ROOT="$root" "$root/app/scripts/set-channel-mode.sh" "$old_channel_mode"
channel_quiesced=0
persist_env_value ERDAI_RELEASE_IMAGE "$release_image"
if [ -n "$old_container" ]; then docker rm "$old_container" >/dev/null 2>&1 || true; fi
if [ -n "$old_image_id" ] && [ "$old_image_id" != "$core_image_id" ]; then docker image rm "$old_image_id" >/dev/null 2>&1 || true; fi
rollback_armed=0
printf 'release=%s\nimage=%s\nschema=%s\nchannel_mode=%s\n' "$release" "$release_image" "$schema" "$old_channel_mode"
