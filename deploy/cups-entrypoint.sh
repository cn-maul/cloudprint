#!/bin/sh
# CloudPrint needs a local CUPS daemon: it owns the PPD and the vendor filter.
# The container runs both processes, with cupsd in the background.
set -e

if command -v cupsd >/dev/null 2>&1; then
  # /etc/cups may be a bind mount from the host, which starts empty. Seed it
  # from the image so the spooler has a usable default configuration.
  if [ ! -f /etc/cups/cupsd.conf ] && [ -d /usr/share/cloudprint/cups-default ]; then
    cp -an /usr/share/cloudprint/cups-default/. /etc/cups/ 2>/dev/null || true
  fi
  # Debian ships a default that listens on all interfaces; rewrite only the
  # TCP entries so the spooler is reachable from inside this container alone.
  # The "Listen /run/cups/cups.sock" line must stay: CloudPrint's admin calls
  # use the Unix socket, which authenticates as a local lpadmin member.
  sed -i -E '/^Listen [^\/]/s/.*/Listen 127.0.0.1:631/' /etc/cups/cupsd.conf 2>/dev/null || true
  grep -q '^Listen 127\.0\.0\.1:631' /etc/cups/cupsd.conf ||
    echo 'Listen 127.0.0.1:631' >> /etc/cups/cupsd.conf
  grep -q '^Listen /run/cups/cups.sock' /etc/cups/cupsd.conf ||
    echo 'Listen /run/cups/cups.sock' >> /etc/cups/cupsd.conf
  if ! grep -qs '^ServerAlias \*' /etc/cups/cupsd.conf; then
    echo 'ServerAlias *' >> /etc/cups/cupsd.conf
  fi
  # lpadmin authenticates with the root certificate; loopback is local admin.
  if ! grep -qs '^SystemGroup ' /etc/cups/cupsd.conf; then
    echo 'SystemGroup lpadmin root' >> /etc/cups/cupsd.conf
  fi
  # cupsd refuses to start if its certificate directory is missing.
  mkdir -p /run/cups/certs
  chown root:lpadmin /run/cups/certs 2>/dev/null || true
  chmod 755 /run/cups/certs 2>/dev/null || true

  # Bind-mounted spool and cache directories arrive owned by the host user.
  # cupsd runs as root but expects root:lp ownership here, and the filter
  # runs as lp.
  for d in /var/spool/cups /var/cache/cups; do
    [ -d "$d" ] || continue
    chown -R root:lp "$d" 2>/dev/null || true
  done
  chmod 710 /var/spool/cups 2>/dev/null || true
  chmod 770 /var/cache/cups 2>/dev/null || true
  chmod 1770 /var/spool/cups/tmp 2>/dev/null || true

  # The vendor driver chain reads the queue PPD as cloudprint (member of lp).
  # A bind-mounted /etc/cups can carry host ownership, so make the group and
  # mode predictable instead of failing later with "permission denied".
  if [ -d /etc/cups/ppd ]; then
    chgrp lp /etc/cups/ppd/*.ppd 2>/dev/null || true
    chmod 640 /etc/cups/ppd/*.ppd 2>/dev/null || true
  fi

  # /data is created at image build time, so a bind mount from the host can
  # arrive with a different owner. CloudPrint runs as cloudprint, so hand the
  # mounted directory over before dropping privileges.
  if [ -d /data ]; then
    chown -R cloudprint:cloudprint /data 2>/dev/null || true
  fi

  echo 'starting cupsd'
  /usr/sbin/cupsd -f &
  CUPS_PID=$!
  i=0
  while [ ! -S /run/cups/cups.sock ] && [ $i -lt 50 ]; do
    sleep 0.2
    i=$((i + 1))
  done
  if [ ! -S /run/cups/cups.sock ]; then
    echo 'cupsd did not start; printers that need a driver will be unavailable' >&2
  fi
  trap 'kill $CUPS_PID 2>/dev/null || true' INT TERM
else
  echo 'cupsd is not installed; only PDF-capable printers can be used' >&2
fi

# The base image keeps HOME=/root. After setpriv drops to cloudprint that
# directory is unwritable, and libraries such as pdfcpu create per-user config
# and cache below it. Point them at the service home instead.
export HOME=/home/cloudprint
export XDG_CONFIG_HOME=/home/cloudprint/.config
export XDG_CACHE_HOME=/home/cloudprint/.cache
mkdir -p "$HOME" "$XDG_CONFIG_HOME" "$XDG_CACHE_HOME" 2>/dev/null || true
chown -R cloudprint:cloudprint "$HOME" 2>/dev/null || true

# CloudPrint runs unprivileged. Adding it to lpadmin lets it create and delete
# queues through cupsd without a password prompt.
exec setpriv --reuid=cloudprint --regid=cloudprint --init-groups /app/cloudprint "$@"
