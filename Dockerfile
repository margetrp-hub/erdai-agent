FROM node:24-alpine AS webui

WORKDIR /src
COPY go/webui/package.json go/webui/package-lock.json ./
RUN npm ci
COPY go/webui/ ./
RUN npm run build

FROM golang:1.26.5-alpine AS source

ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
ENV GOGC=100 \
	GOMEMLIMIT=600MiB \
	GOMAXPROCS=1

WORKDIR /src
COPY go/go.mod go/go.sum ./
RUN /usr/local/go/bin/go mod download
COPY go/ ./
COPY --from=webui /src/dist ./webui/dist

FROM source AS verify
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=tmpfs,target=/tmp \
    /usr/local/go/bin/go test -p=1 -timeout 20m ./... \
    && /usr/local/go/bin/go vet -p=1 ./...

FROM source AS build
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=tmpfs,target=/tmp \
    CGO_ENABLED=0 GOOS=linux /usr/local/go/bin/go build -p=1 -trimpath -ldflags="-s -w" -o /out/root/app/erdai-agent . \
    && mkdir -p /out/root/app/data/media /out/root/etc/ssl/certs \
    && cp /etc/ssl/certs/ca-certificates.crt /out/root/etc/ssl/certs/ca-certificates.crt \
    && ln -s /app/data/media /out/root/erdai-media \
    && chown -R 1000:1000 /out/root/app

FROM source AS verify-race
RUN apk add --no-cache gcc musl-dev
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=tmpfs,target=/tmp \
    CGO_ENABLED=1 /usr/local/go/bin/go test -race -p=1 -timeout 20m \
    -run 'Test(Hardening|Optimization|Repair)' .

FROM scratch AS final

ARG ERDAI_RELEASE_VERSION=dev
ARG ERDAI_SOURCE_REVISION=unknown

LABEL org.opencontainers.image.title="ErDai Agent" \
      org.opencontainers.image.version="${ERDAI_RELEASE_VERSION}" \
      org.opencontainers.image.revision="${ERDAI_SOURCE_REVISION}" \
      org.opencontainers.image.description="ErDai Agent pure Go runtime with native platform connectors"

COPY --from=build /out/root /

ENV ERDAI_CORE_LISTEN=0.0.0.0:6280 \
    ERDAI_ADMIN_LISTEN=0.0.0.0:6282 \
    ERDAI_CONFIG_DATABASE=/app/data/erdai-agent-core.sqlite3 \
	ERDAI_RUNTIME_DATABASE=/app/data/erdai-agent-core.sqlite3 \
	ERDAI_LEGACY_RUNTIME_DATABASE=/app/data/erdai-runtime.sqlite3 \
	ERDAI_MEDIA_DIR=/app/data/media \
	TZ=Asia/Shanghai

USER 1000:1000
WORKDIR /app

EXPOSE 6280 6282
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD ["/app/erdai-agent", "--health-check"]

ENTRYPOINT ["/app/erdai-agent"]
