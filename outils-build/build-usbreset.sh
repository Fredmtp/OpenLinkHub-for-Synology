#!/bin/bash
set -e
mkdir -p ~/usbreset
cp ./usbreset/main.go ~/usbreset/main.go 2>/dev/null || true
cd ~/usbreset
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags '-s -w' -o usbreset .
sha256sum usbreset
# essais sans danger: mauvais argument, chemin factice, identité différente
echo "-- sans argument"; ./usbreset || true
mkdir -p /tmp/fakeusb && cd /tmp/fakeusb && echo 1b1c > idVendor && echo 0c3f > idProduct && echo 3 > busnum && echo 2 > devnum
cd ~/usbreset
echo "-- mauvaise identité (QNAP supposé)"; ./usbreset /tmp/fakeusb 1234:5678 || true
echo "-- bonne identité, noeud /dev/bus/usb/003/002 absent sous WSL"; ./usbreset /tmp/fakeusb 1b1c:0c3f || true
