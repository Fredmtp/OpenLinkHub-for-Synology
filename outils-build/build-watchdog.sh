#!/bin/bash
# Compile et teste la surveillance (copiée depuis le projet Windows vers ~/watchdog).
set -e
cd ~/watchdog
gofmt -l . || true
echo "== vet"; go vet ./...
echo "== tests"; go test ./... 2>&1 | tail -40
echo "== build"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags '-s -w' -o olh-watchdog .
file olh-watchdog
sha256sum olh-watchdog
