ARG ERDAI_MONITOR_BROWSER_BASE_IMAGE=docker.io/chromedp/headless-shell@sha256:5f877a2a559dea1a99fb750da695d28a020cdd49db660aead6c78a46e3c7dd50
FROM ${ERDAI_MONITOR_BROWSER_BASE_IMAGE}

ARG ERDAI_RELEASE_VERSION=dev
ARG ERDAI_SOURCE_REVISION=unknown

ENV TZ=Asia/Shanghai

RUN LANG=C.UTF-8 DEBIAN_FRONTEND=noninteractive apt-get update \
    && apt-get install -y --no-install-recommends fonts-noto-cjk \
    && rm -rf /var/lib/apt/lists/*

LABEL org.opencontainers.image.title="ErDai Monitor Browser" \
      org.opencontainers.image.version="${ERDAI_RELEASE_VERSION}" \
      org.opencontainers.image.revision="${ERDAI_SOURCE_REVISION}" \
      org.opencontainers.image.description="Private headless browser for Sub2API monitor card capture"
