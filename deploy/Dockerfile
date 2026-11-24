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
# Runtime packages only. The image used to install fonts-noto-cjk (89 MB),
# openprinting-ppds (7 MB) and printer-driver-all; the last one installs
# nothing here because every driver it pulls in is a Recommends and this
# build passes --no-install-recommends.
#
# What is left:
#   - CUPS daemon/client plus the filter chain CloudPrint invokes directly
#   - Ghostscript + poppler for raster conversion and PDF previews
#   - fonts-wqy-microhei (5 MB) instead of Noto CJK (89 MB): Chinese text
#     still renders, just without Noto's full glyph coverage
#   - No PPD packs. Printers are driven by the vendor PPD installed by
#     install-pantum-driver.sh, or by the driverless "-m everywhere" queue.
#
# Adding openprinting-ppds back costs about 29 MB (it pulls in python3);
# do it only when a HP/Brother/Epson model without a vendor PPD is needed.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       ca-certificates tzdata poppler-utils wget curl unzip \
       cups cups-client cups-filters cups-ipp-utils \
       ghostscript fontconfig fonts-wqy-microhei \
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
