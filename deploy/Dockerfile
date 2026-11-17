FROM node:22-alpine AS web
WORKDIR /src
COPY web/package*.json ./web/
RUN cd web && npm ci
COPY web/ ./web/
RUN cd web && npm run build

FROM golang:1.27.4-bookworm AS build
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN rm -rf ./cmd/cloudprint/ui && mkdir -p ./cmd/cloudprint/ui
COPY --from=web /src/backend/cmd/cloudprint/ui/ ./cmd/cloudprint/ui/
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/cloudprint ./cmd/cloudprint

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       ca-certificates tzdata libreoffice fonts-noto-cjk poppler-utils wget \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --create-home --home-dir /home/cloudprint cloudprint
WORKDIR /app
COPY --from=build /out/cloudprint /app/cloudprint
RUN mkdir -p /data && chown -R cloudprint:cloudprint /data /app
USER cloudprint
ENV CLOUDPRINT_ADDR=:8080 \
    CLOUDPRINT_DATA=/data \
    CLOUDPRINT_LIBREOFFICE=/usr/bin/soffice \
    CLOUDPRINT_PDFTOPPM=/usr/bin/pdftoppm \
    TZ=Asia/Shanghai
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=3s --start-period=20s \
    CMD wget -q -O - http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/app/cloudprint"]
