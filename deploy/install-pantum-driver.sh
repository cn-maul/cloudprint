#!/bin/sh
# Installs the official Pantum Linux driver (PPDs + CUPS filters + /opt/pantum
# libraries) into the machine that runs cupsd. The vendor package is downloaded
# from Pantum's own CDN; nothing is bundled in this repository, so the vendor
# licence still applies.
#
#   sudo ./deploy/install-pantum-driver.sh           # install into /
#   ./deploy/install-pantum-driver.sh --root /tmp/x  # stage into an image root
#
# The Dockerfile calls it with --root / at build time.
set -e

VERSION="${PANTUM_VERSION:-V1_1_186}"
URL="${PANTUM_URL:-https://drivers.pantum.com/userfiles/files/download/drive/5103/Pantum%20Linux%20Driver%20V1_1_186.zip}"
ROOT="/"

while [ $# -gt 0 ]; do
  case "$1" in
    --root) ROOT="${2%/}"; shift 2 ;;
    --root=*) ROOT="${1#--root=}"; ROOT="${ROOT%/}"; shift ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done
[ -n "$ROOT" ] || ROOT=""

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "缺少 $1，请先安装（curl / unzip / dpkg）" >&2; exit 1; }
}
need curl
need unzip
need dpkg-deb

echo "下载奔图 Linux 驱动 $VERSION"
curl -fsSL -H 'Referer: https://global.pantum.com/support/download/driver/' -o "$WORK/driver.zip" "$URL"
unzip -q "$WORK/driver.zip" -d "$WORK/pkg"
DEB="$(find "$WORK/pkg" -name 'pantum_*.deb' | head -n1)"
if [ -z "$DEB" ]; then
  echo "驱动包里没有找到 pantum_*.deb" >&2
  exit 1
fi
dpkg-deb -x "$DEB" "$WORK/root"

echo "安装 PPD 到 $ROOT/usr/share/cups/model/Pantum"
mkdir -p "$ROOT/usr/share/cups/model/Pantum"
cp "$WORK"/root/usr/share/cups/model/Pantum/*.ppd "$ROOT/usr/share/cups/model/Pantum/"

echo "安装 CUPS 过滤器到 $ROOT/usr/lib/cups/filter"
mkdir -p "$ROOT/usr/lib/cups/filter"
for f in "$WORK"/root/usr/lib/cups/filter/*; do
  [ -f "$f" ] || continue
  install -m 755 "$f" "$ROOT/usr/lib/cups/filter/"
done
# Some vendor builds ship non-executable filters inside the deb; cupsd needs
# the exec bit or the job dies with "execv failed: Permission denied".
chmod 755 "$ROOT"/usr/lib/cups/filter/* 2>/dev/null || true

echo "安装驱动运行库到 $ROOT/opt/pantum"
if [ -d "$WORK/root/opt/pantum" ]; then
  mkdir -p "$ROOT/opt"
  cp -a "$WORK/root/opt/pantum" "$ROOT/opt/"
  mkdir -p "$ROOT/usr/lib/x86_64-linux-gnu"
  for lib in "$ROOT"/opt/pantum/lib/*.so*; do
    [ -e "$lib" ] || continue
    ln -sf "$lib" "$ROOT/usr/lib/x86_64-linux-gnu/$(basename "$lib")" 2>/dev/null || true
  done
fi

mkdir -p "$ROOT/usr/lib/x86_64-linux-gnu"
for d in "$WORK"/root/usr/lib/x86_64-linux-gnu/*; do
  [ -e "$d" ] || continue
  cp -a "$d" "$ROOT/usr/lib/x86_64-linux-gnu/"
done

if [ "$ROOT" = "" ] || [ "$ROOT" = "/" ]; then
  if command -v ldconfig >/dev/null 2>&1; then ldconfig; fi
fi

echo "完成：$(find "$ROOT/usr/share/cups/model/Pantum" -name '*.ppd' | wc -l) 个 PPD 已安装"
echo "重启 CloudPrint / cupsd 后，在“打印机管理”里重新接入即可自动匹配型号。"
