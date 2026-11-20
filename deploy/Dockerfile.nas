# Runtime-only image for hosts that cannot build the Go/Node stages (no
# reliable Docker Hub access). The binary is cross-compiled on the dev machine
# and the vendor driver payload sits next to this file.
#
# Build context layout:
#   cloudprint            linux/amd64 binary
#   cups-entrypoint.sh
#   pantum/               optional vendor payload (PPDs, filters, /opt/pantum)
FROM debian:bookworm-slim

ARG http_proxy=""
ARG https_proxy=""
ARG HTTP_PROXY=""
ARG HTTPS_PROXY=""

ARG INSTALL_LIBREOFFICE=0

# V1 accepts PDF/JPEG/PNG only, so no Office runtime is installed. If the
# suite is ever added back, fonts-noto-cjk must come with it or Chinese
# documents convert to a PDF full of empty boxes.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       ca-certificates tzdata poppler-utils wget \
       fonts-liberation fonts-dejavu-core \
       cups cups-client cups-filters cups-ipp-utils \
       printer-driver-all openprinting-ppds ghostscript \
       libcups2 libcupsimage2 libusb-1.0-0 \
    && rm -rf /var/lib/apt/lists/* \
    # lp is needed to read /etc/cups/ppd/<queue>.ppd (root:lp 0640), lpadmin to
    # manage queues over the local socket. Without lp the vendor driver chain
    # can only fail with a permission error.
    && useradd --system --create-home --home-dir /home/cloudprint \
         --groups lpadmin,lp cloudprint

COPY cups-entrypoint.sh /usr/local/bin/cups-entrypoint.sh
RUN chmod +x /usr/local/bin/cups-entrypoint.sh \
    && mkdir -p /etc/cups/drivers /var/spool/cups /var/cache/cups /var/log/cups \
    && mkdir -p /usr/share/cloudprint/cups-default \
    && cp -a /etc/cups/. /usr/share/cloudprint/cups-default/

# Vendor driver payload: PPDs, CUPS filters and the vendor runtime libraries.
COPY pantum/ /
RUN chmod 755 /usr/lib/cups/filter/* 2>/dev/null || true; \
    if [ -d /opt/pantum/lib ]; then \
      for lib in /opt/pantum/lib/*.so*; do \
        [ -e "$lib" ] || continue; \
        ln -sf "$lib" "/usr/lib/x86_64-linux-gnu/$(basename "$lib")"; \
      done; \
      ldconfig; \
    fi

WORKDIR /app
COPY cloudprint /app/cloudprint
RUN chmod +x /app/cloudprint && mkdir -p /data && chown -R cloudprint:cloudprint /data /app

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
