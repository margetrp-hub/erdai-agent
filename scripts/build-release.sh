#!/bin/sh
set -eu

release=${1:?usage: build-release.sh RELEASE SCHEMA OUTPUT_DIR}
schema=${2:?usage: build-release.sh RELEASE SCHEMA OUTPUT_DIR}
output_dir=${3:?usage: build-release.sh RELEASE SCHEMA OUTPUT_DIR}
platform=linux/amd64
image=erdai-agent:$release
embedding_image=${ERDAI_EMBEDDING_IMAGE:-ghcr.io/ggml-org/llama.cpp@sha256:046ad33efae5c4ec3e19be344778a80c2bb402d3f10b464514c5fe99eab94d19}

case "$release" in
  *[!A-Za-z0-9._-]*|'') echo "release must contain only A-Za-z0-9._-" >&2; exit 2 ;;
esac
case "$schema" in
  *[!0-9]*|'') echo "schema must be a positive integer" >&2; exit 2 ;;
esac

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
runtime_version=$(sed -n 's/^const erdaiRuntimeVersion = "\([0-9][0-9.]*\)"$/\1/p' "$root/go/version.go")
[ -n "$runtime_version" ] || { echo "could not read runtime version" >&2; exit 1; }
[ "${release#v}" = "$runtime_version" ] || {
  echo "release $release does not match runtime version $runtime_version" >&2
  exit 1
}
output_dir=$(mkdir -p "$output_dir" && CDPATH= cd -- "$output_dir" && pwd)
bundle=$output_dir/erdai-agent-$release
archive=$output_dir/erdai-agent-stable-$release.tar.gz
test ! -e "$bundle"
test ! -e "$archive"

command -v docker >/dev/null
command -v sha256sum >/dev/null
command -v npm >/dev/null
if git -C "$root" ls-files --error-unmatch Dockerfile >/dev/null 2>&1; then
  test -z "$(git -C "$root" status --porcelain -- .github Dockerfile compose.production.yml runtime.env.example go scripts)" || {
    echo "release source has uncommitted release-pipeline changes" >&2
    exit 1
  }
  source_revision=$(git -C "$root" rev-parse HEAD)
else
  source_revision=
fi

(cd "$root/go/webui" && npm ci && npm run build)

if [ -z "$source_revision" ]; then
  source_revision=$(tar -cf - --sort=name --mtime='UTC 1970-01-01' --owner=0 --group=0 --numeric-owner \
    --exclude=go/webui/node_modules --exclude=go/webui/output --exclude=go/webui/.vite \
    -C "$root" Dockerfile compose.production.yml runtime.env.example go runtime scripts | sha256sum | awk '{print "tree-" $1}')
fi

docker build --platform "$platform" --target verify --progress=plain "$root"
docker build --platform "$platform" --progress=plain \
  --build-arg "ERDAI_RELEASE_VERSION=$release" \
  --build-arg "ERDAI_SOURCE_REVISION=$source_revision" \
  --tag "$image" "$root"
docker pull "$embedding_image"

install -d -m 755 "$bundle/app/scripts"
cp "$root/compose.production.yml" "$root/runtime.env.example" "$bundle/app/"
cp "$root/scripts/deploy-250.sh" "$root/scripts/verify-production.sh" \
  "$root/scripts/set-channel-mode.sh" "$root/scripts/erdai-update-agent.sh" \
  "$root/scripts/install-update-agent.sh" "$bundle/app/scripts/"
chmod 755 "$bundle/app/scripts/"*.sh

docker image inspect -f '{{.Id}}' "$image" > "$bundle/core-image-id"
docker image inspect -f '{{.Id}}' "$embedding_image" > "$bundle/embedding-image-id"
docker save --output "$bundle/images.tar" "$image" "$embedding_image"
tar -czf "$bundle/app.tar.gz" --owner=0 --group=0 --numeric-owner -C "$bundle/app" .
rm -rf "$bundle/app"

cat > "$bundle/manifest.env" <<EOF
RELEASE_ID=$release
RELEASE_IMAGE=$image
CORE_IMAGE_ID=$(cat "$bundle/core-image-id")
EMBEDDING_IMAGE=$embedding_image
EMBEDDING_IMAGE_ID=$(cat "$bundle/embedding-image-id")
SCHEMA_VERSION=$schema
PLATFORM=$platform
SOURCE_REVISION=$source_revision
CORE_MEMORY_LIMIT_BYTES=536870912
EMBEDDING_MEMORY_LIMIT_BYTES=469762048
MEMORY_LIMIT_TOTAL_BYTES=1006632960
EOF
rm -f "$bundle/core-image-id" "$bundle/embedding-image-id"
(cd "$bundle" && sha256sum manifest.env images.tar app.tar.gz > SHA256SUMS)

tar -czf "$archive" -C "$output_dir" "$(basename "$bundle")"

printf 'release_bundle=%s\n' "$bundle"
printf 'release_archive=%s\n' "$archive"
printf 'release_archive_sha256=%s\n' "$(sha256sum "$archive" | awk '{print $1}')"
printf 'release_image=%s\n' "$image"
printf 'source_revision=%s\n' "$source_revision"
