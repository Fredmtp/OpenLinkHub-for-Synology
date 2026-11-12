#!/bin/bash
# Comme build2.sh, avec -DNO_ICONV: go-hid lit les chaines USB (nom, serie) sans iconv
# (iconv ne fonctionne pas dans un binaire glibc statique).
set -e
PFX=$HOME/dsm-deps
cd "$HOME/OpenLinkHub"
echo "== build $(date +%T)"
export PKG_CONFIG_PATH="$PFX/lib/pkgconfig"
export CGO_ENABLED=1
export CGO_CFLAGS="-DNO_ICONV"
export CGO_LDFLAGS="-L$PFX/stub -L$PFX/lib"
go build -tags "libusb noaudio osusergo netgo" \
  -ldflags '-s -w -linkmode external -extldflags "-static"' -o "$HOME/OpenLinkHub-dsm" . 2>&1 | tail -30
ls -la "$HOME/OpenLinkHub-dsm"
file "$HOME/OpenLinkHub-dsm"
mkdir -p "$HOME/olh-dist"
cp "$HOME/OpenLinkHub-dsm" "$HOME/olh-dist/OpenLinkHub"
sha256sum "$HOME/olh-dist/OpenLinkHub"
echo "== done $(date +%T)"
