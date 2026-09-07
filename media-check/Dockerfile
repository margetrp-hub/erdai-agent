FROM python:3.13-alpine
ARG ERDAI_RELEASE_VERSION=dev
ARG ERDAI_SOURCE_REVISION=unknown
LABEL org.opencontainers.image.version=$ERDAI_RELEASE_VERSION org.opencontainers.image.revision=$ERDAI_SOURCE_REVISION
RUN apk add --no-cache ffmpeg && addgroup -g 1000 media && adduser -D -H -u 1000 -G media media
WORKDIR /app
COPY server.py /app/server.py
USER 1000:1000
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
EXPOSE 8091
CMD ["python", "/app/server.py"]
