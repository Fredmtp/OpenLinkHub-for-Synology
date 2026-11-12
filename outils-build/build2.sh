#!/bin/bash
# Etape 1: libusb statique sans udev. Etape 2: OpenLinkHub avec tags libusb/noaudio.
set -e
PFX=$HOME/dsm-deps
mkdir -p "$PFX/stub" "$HOME/dsm-src"
if [ ! -f "$PFX/lib/libusb-1.0.a" ]; then
  cd "$HOME/dsm-src"
  curl -fsSL -o libusb.tar.bz2 https://github.com/libusb/libusb/releases/download/v1.0.29/libusb-1.0.29.tar.bz2
  python3 -c "import tarfile; tarfile.open('libusb.tar.bz2').extractall('.')"
  cd libusb-1.0.29
  ./configure --prefix="$PFX" --enable-static --disable-shared --disable-udev >/dev/null
  make -j"$(nproc)" >/dev/null
  make install >/dev/null
fi
# libudev vide: go-hid ajoute -ludev meme pour le backend libusb, qui n'en a pas besoin
ar rcs "$PFX/stub/libudev.a"

cd "$HOME/OpenLinkHub"
echo "== build $(date +%T)"
export PKG_CONFIG_PATH="$PFX/lib/pkgconfig"
export CGO_ENABLED=1
export CGO_LDFLAGS="-L$PFX/stub -L$PFX/lib"
go build -tags "libusb noaudio osusergo netgo" \
  -ldflags '-s -w -linkmode external -extldflags "-static"' -o "$HOME/OpenLinkHub-dsm" . 2>&1 | tail -30
ls -la "$HOME/OpenLinkHub-dsm"
file "$HOME/OpenLinkHub-dsm"
echo "== done $(date +%T)"
