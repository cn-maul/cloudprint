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
       ca-certificates tzdata fonts-noto-cjk poppler-utils wget curl unzip \
       cups cups-client cups-filters cups-ipp-utils \
       printer-driver-all openprinting-ppds ghostscript \
       libcups2 libcupsimage2 libusb-1.0-0 \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --create-home --home-dir /home/cloudprint --groups lpadmin cloudprint
COPY deploy/cups-entrypoint.sh /usr/local/bin/cups-entrypoint.sh
COPY deploy/install-pantum-driver.sh /usr/local/bin/install-pantum-driver.sh
RUN chmod +x /usr/local/bin/cups-entrypoint.sh \
    && chmod +x /usr/local/bin/install-pantum-driver.sh \
    && mkdir -p /etc/cups/drivers /var/spool/cups /var/cache/cups /var/log/cups \
    && mkdir -p /usr/share/cloudprint/cups-default \
    && cp -a /etc/cups/. /usr/share/cloudprint/cups-default/
# /var/spool/cups keeps its Debian ownership (root:lp): cupsd runs the vendor
# filters as `lp`, so handing the spool to cloudprint would break printing.

# Vendor drivers are installed at build time so a fresh container can print
# without a manual step. The download is pinned and lives outside this repo.
ARG INSTALL_PANTUM=1
RUN if [ "$INSTALL_PANTUM" = "1" ]; then \
      /usr/local/bin/install-pantum-driver.sh --root / || \
      echo 'Pantum 驱动下载失败，镜像继续构建；可稍后手动执行 install-pantum-driver.sh' >&2; \
    fi

WORKDIR /app
COPY --from=build /out/cloudprint /app/cloudprint
RUN mkdir -p /data && chown -R cloudprint:cloudprint /data /app
ENV CLOUDPRINT_ADDR=:8080 \
    CLOUDPRINT_DATA=/data \
    CLOUDPRINT_PDFTOPPM=/usr/bin/pdftoppm \
    CLOUDPRINT_CUPS_HOST=127.0.0.1:631 \
    HOME=/home/cloudprint \
    TZ=Asia/Shanghai
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=3s --start-period=20s \
    CMD wget -q -O - http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/cups-entrypoint.sh"]
