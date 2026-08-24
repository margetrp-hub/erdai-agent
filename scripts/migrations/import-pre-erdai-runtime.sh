#!/bin/sh
set -eu

target_root=${ERDAI_INSTALL_ROOT:-/opt/erdai-agent}
legacy_combined_root=/opt/doubao-agent-core
legacy_go_root=/opt/erdai-agent-web
legacy_channel_root=/opt/astrbot
core_env=$target_root/.env
runtime_env=$target_root/runtime.env
compose_file=${ERDAI_COMPOSE_FILE:-$target_root/app/compose.production.yml}
migration_backup_root=${ERDAI_MIGRATION_BACKUP_ROOT:-$target_root/backups/pre-erdai-import}

if [ "$(id -u)" -ne 0 ]; then
  echo "Run this script as root." >&2
  exit 1
fi

for container in erdai-agent erdai-agent-runtime erdai-agent-core astrbot doubao-agent-core erdai-agent-web; do
  if docker ps --format '{{.Names}}' | grep -qx "$container"; then
    echo "Stop $container before importing data and secrets." >&2
    exit 1
  fi
done

install -d -o 1000 -g 1000 -m 700 "$target_root/data"
install -d -o 1000 -g 1000 -m 750 "$target_root/data/media"
install -d -m 700 "$target_root/data/runtime"
install -d -m 700 "$migration_backup_root"

backup_sqlite() {
  sqlite_source_path=$1
  sqlite_target_path=$2
  [ -f "$sqlite_source_path" ] || return 1
  python3 - "$sqlite_source_path" "$sqlite_target_path" <<'PY'
import os
import sqlite3
import sys

source_path, target_path = sys.argv[1:]
temporary = target_path + ".next"
if os.path.exists(temporary):
    os.remove(temporary)
with sqlite3.connect(f"file:{source_path}?mode=ro", uri=True) as source:
    try:
        with sqlite3.connect(temporary) as target:
            source.backup(target)
            if target.execute("PRAGMA integrity_check").fetchone()[0] != "ok":
                raise SystemExit(f"integrity check failed for {source_path}")
    except BaseException:
        try:
            os.remove(temporary)
        except FileNotFoundError:
            pass
        raise
os.replace(temporary, target_path)
for suffix in ("-wal", "-shm"):
    try:
        os.remove(target_path + suffix)
    except FileNotFoundError:
        pass
PY
}

import_database_once() {
  target_path=$1
  marker_path=$2
  backup_label=$3
  shift 3

  if [ -f "$marker_path" ]; then
    backup_sqlite "$target_path" "$migration_backup_root/$backup_label-verified.sqlite3"
    rm -f "$migration_backup_root/$backup_label-verified.sqlite3"
    return 0
  fi

  target_valid=0
  if [ -f "$target_path" ]; then
    cp -p "$target_path" "$migration_backup_root/$backup_label-preexisting.raw"
    for suffix in -wal -shm; do
      if [ -f "$target_path$suffix" ]; then
        cp -p "$target_path$suffix" \
          "$migration_backup_root/$backup_label-preexisting.raw$suffix"
      fi
    done
    if backup_sqlite "$target_path" \
      "$migration_backup_root/$backup_label-preexisting.sqlite3"; then
      target_valid=1
    fi
  fi

  imported=0
  for source_path in "$@"; do
    if [ "$source_path" != "$target_path" ] && \
      backup_sqlite "$source_path" "$target_path"; then
      printf '%s\n' "$source_path" > "$migration_backup_root/$backup_label-imported-from.txt"
      imported=1
      break
    fi
  done

  if [ "$imported" -ne 1 ] && [ "$target_valid" -ne 1 ]; then
    echo "No valid source was found for $target_path." >&2
    exit 1
  fi

  install -m 600 /dev/null "$marker_path"
}

import_database_once \
  "$target_root/data/erdai-agent-core.sqlite3" \
  "$target_root/data/.pre-erdai-core-import-v1" \
  core \
  "$legacy_combined_root/data/doubao-agent-core.sqlite3" \
  "$legacy_combined_root/data/erdai-agent-core.sqlite3" \
  "$legacy_go_root/data/erdai-agent-core.sqlite3" \
  "$legacy_go_root/data/doubao-agent-core.sqlite3" \
  "$legacy_combined_root/erdai-agent-core.sqlite3" \
  "$legacy_combined_root/doubao-agent-core.sqlite3" \
  "$legacy_go_root/erdai-agent-core.sqlite3" \
  "$legacy_go_root/doubao-agent-core.sqlite3"

import_database_once \
  "$target_root/data/erdai-runtime.sqlite3" \
  "$target_root/data/.pre-erdai-runtime-import-v1" \
  runtime \
  "$legacy_combined_root/data/erdai-runtime.sqlite3" \
  "$legacy_go_root/data/erdai-runtime.sqlite3" \
  "$legacy_combined_root/erdai-runtime.sqlite3" \
  "$legacy_go_root/erdai-runtime.sqlite3"

for media_root in "$legacy_combined_root/data/media" "$legacy_go_root/data/media"; do
  if [ -d "$media_root" ]; then
    cp -an "$media_root/." "$target_root/data/media/"
  fi
done

if [ -d "$legacy_channel_root/data" ] && [ ! -e "$target_root/data/runtime/data_v4.db" ]; then
  cp -a "$legacy_channel_root/data/." "$target_root/data/runtime/"
fi

chown -R 1000:1000 "$target_root/data"

env_value() {
  file=$1
  name=$2
  [ -r "$file" ] || return 0
  sed -n "s/^${name}=//p" "$file" | head -n 1
}

first_value() {
  name=$1
  shift
  for file in "$@"; do
    value=$(env_value "$file" "$name")
    if [ -n "$value" ]; then
      printf '%s' "$value"
      return 0
    fi
  done
}

first_renamed_value() {
  current_name=$1
  previous_name=$2
  shift 2
  value=$(first_value "$current_name" "$@")
  if [ -z "$value" ]; then
    value=$(first_value "$previous_name" "$@")
  fi
  printf '%s' "$value"
}

require_length() {
  name=$1
  minimum=$2
  value=$3
  if [ "${#value}" -lt "$minimum" ]; then
    echo "$name is missing or too short." >&2
    exit 1
  fi
}

admin_token=$(first_value ERDAI_ADMIN_TOKEN "$core_env" "$legacy_combined_root/.env" "$legacy_go_root/.env")
runtime_token=$(first_value ERDAI_RUNTIME_TOKEN "$core_env" "$legacy_combined_root/.env" "$legacy_go_root/.env")
provider_key=$(first_renamed_value ERDAI_MODEL_API_KEY ASTRBOT_PROVIDER_API_KEY "$core_env" "$legacy_combined_root/.env" "$legacy_channel_root/.env" "$legacy_go_root/.env")
grok_key=$(first_renamed_value ERDAI_GROK_API_KEY ASTRBOT_GROK_API_KEY "$core_env" "$legacy_combined_root/.env" "$legacy_channel_root/.env" "$legacy_go_root/.env")
image_key=$(first_renamed_value ERDAI_IMAGE_API_KEY ASTRBOT_IMAGE_API_KEY "$core_env" "$legacy_combined_root/.env" "$legacy_channel_root/.env" "$legacy_go_root/.env")
ops_token=$(first_renamed_value ERDAI_OPS_TOKEN ASTRBOT_OPS_STATUS_TOKEN "$core_env" "$legacy_combined_root/.env" "$legacy_channel_root/.env" "$legacy_go_root/.env")
qq_secret=$(first_renamed_value ERDAI_QQ_SECRET ASTRBOT_QQ_SECRET "$core_env" "$runtime_env" "$legacy_channel_root/.env" "$legacy_combined_root/.env" "$legacy_go_root/.env")
qq_admin_openids=$(first_value ERDAI_QQ_ADMIN_OPENIDS "$core_env" "$runtime_env" "$legacy_channel_root/.env" "$legacy_combined_root/.env" "$legacy_go_root/.env")

require_length ERDAI_ADMIN_TOKEN 32 "$admin_token"
require_length ERDAI_RUNTIME_TOKEN 32 "$runtime_token"
require_length ERDAI_MODEL_API_KEY 20 "$provider_key"
require_length ERDAI_GROK_API_KEY 20 "$grok_key"
require_length ERDAI_IMAGE_API_KEY 20 "$image_key"
require_length ERDAI_OPS_TOKEN 16 "$ops_token"

encryption_key=$(first_value ERDAI_RUN_ENCRYPTION_KEY "$core_env" "$legacy_combined_root/.env" "$legacy_go_root/.env")
if [ "${#encryption_key}" -lt 32 ]; then
  encryption_key=$(openssl rand -base64 32 | tr -d '\n')
fi
identity_secret=$(first_renamed_value \
  ERDAI_RUNTIME_IDENTITY_SECRET DOUBAO_TRANSPORT_IDENTITY_SECRET \
  "$runtime_env" "$legacy_channel_root/.env" "$core_env" \
  "$legacy_combined_root/.env" "$legacy_go_root/.env")
if [ "${#identity_secret}" -lt 32 ]; then
  identity_secret=$(openssl rand -hex 32)
fi
search_base_url=$(first_value ERDAI_SEARCH_BASE_URL "$core_env" "$legacy_combined_root/.env" "$legacy_go_root/.env")

next_core_env=$(mktemp "$target_root/.env.XXXXXX")
chmod 600 "$next_core_env"
for source_env in "$legacy_go_root/.env" "$legacy_combined_root/.env" "$core_env"; do
  [ -r "$source_env" ] || continue
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      ERDAI_ADMIN_TOKEN=*|ERDAI_RUNTIME_TOKEN=*|ERDAI_RUN_ENCRYPTION_KEY=*|ERDAI_RUNTIME_IDENTITY_SECRET=*|ERDAI_MODEL_API_KEY=*|ERDAI_GROK_API_KEY=*|ERDAI_IMAGE_API_KEY=*|ERDAI_OPS_TOKEN=*|ERDAI_QQ_SECRET=*|ERDAI_QQ_ADMIN_OPENIDS=*|ERDAI_RUNTIME_ENV_FILE=*|ERDAI_SEARCH_BASE_URL=*|ERDAI_CORE_URL=*|ERDAI_CORE_RUNTIME_TOKEN=*) ;;
      ASTRBOT_PROVIDER_API_KEY=*|ASTRBOT_GROK_API_KEY=*|ASTRBOT_IMAGE_API_KEY=*|ASTRBOT_OPS_STATUS_TOKEN=*|ASTRBOT_QQ_SECRET=*) ;;
      DOUBAO_AGENT_CORE_URL=*|DOUBAO_AGENT_CORE_TOKEN=*|DOUBAO_TRANSPORT_IDENTITY_SECRET=*) ;;
      *) printf '%s\n' "$line" ;;
    esac
  done < "$source_env"
done > "$next_core_env"
{
  printf 'ERDAI_ADMIN_TOKEN=%s\n' "$admin_token"
  printf 'ERDAI_RUNTIME_TOKEN=%s\n' "$runtime_token"
  printf 'ERDAI_RUN_ENCRYPTION_KEY=%s\n' "$encryption_key"
  printf 'ERDAI_RUNTIME_IDENTITY_SECRET=%s\n' "$identity_secret"
  printf 'ERDAI_MODEL_API_KEY=%s\n' "$provider_key"
  printf 'ERDAI_GROK_API_KEY=%s\n' "$grok_key"
  printf 'ERDAI_IMAGE_API_KEY=%s\n' "$image_key"
  printf 'ERDAI_OPS_TOKEN=%s\n' "$ops_token"
  if [ -n "$qq_secret" ]; then
    printf 'ERDAI_QQ_SECRET=%s\n' "$qq_secret"
  fi
  if [ -n "$qq_admin_openids" ]; then
    printf 'ERDAI_QQ_ADMIN_OPENIDS=%s\n' "$qq_admin_openids"
  fi
  printf 'ERDAI_RUNTIME_ENV_FILE=%s\n' "$runtime_env"
  if [ -n "$search_base_url" ]; then
    printf 'ERDAI_SEARCH_BASE_URL=%s\n' "$search_base_url"
  fi
} >> "$next_core_env"
mv "$next_core_env" "$core_env"
chmod 600 "$core_env"

next_runtime_env=$(mktemp "$target_root/runtime.env.XXXXXX")
chmod 600 "$next_runtime_env"
for source_env in "$legacy_channel_root/.env" "$runtime_env"; do
  [ -r "$source_env" ] || continue
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      ''|'#'*) printf '%s\n' "$line" ;;
      ERDAI_CORE_URL=*|ERDAI_CORE_RUNTIME_TOKEN=*|ERDAI_RUNTIME_IDENTITY_SECRET=*|DOUBAO_AGENT_CORE_URL=*|DOUBAO_AGENT_CORE_TOKEN=*|DOUBAO_TRANSPORT_IDENTITY_SECRET=*) ;;
      ASTRBOT_PROVIDER_API_KEY=*|ASTRBOT_GROK_API_KEY=*|ASTRBOT_IMAGE_API_KEY=*|ASTRBOT_OPS_STATUS_TOKEN=*|ASTRBOT_QQ_SECRET=*|ERDAI_MODEL_API_KEY=*|ERDAI_GROK_API_KEY=*|ERDAI_IMAGE_API_KEY=*|ERDAI_OPS_TOKEN=*|ERDAI_QQ_SECRET=*|ERDAI_QQ_ADMIN_OPENIDS=*) ;;
      *) printf '%s\n' "$line" ;;
    esac
  done < "$source_env"
done > "$next_runtime_env"
{
  printf 'ERDAI_CORE_URL=http://127.0.0.1:6280\n'
  printf 'ERDAI_CORE_RUNTIME_TOKEN=%s\n' "$runtime_token"
  printf 'ERDAI_RUNTIME_IDENTITY_SECRET=%s\n' "$identity_secret"
} >> "$next_runtime_env"
mv "$next_runtime_env" "$runtime_env"
chmod 600 "$runtime_env"

if [ ! -f "$compose_file" ] && [ -f "$target_root/compose.production.yml" ]; then
  compose_file=$target_root/compose.production.yml
fi
test -f "$compose_file"
docker compose --env-file "$core_env" -f "$compose_file" config -q
echo "ErDai Agent data, environment scopes, and unified Compose are valid."
