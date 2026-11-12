#!/bin/bash
set -e
cd ~/watchdog
gofmt -w *.go
echo "formatage restant: [$(gofmt -l .)]"
go vet ./...
go test -count=1 -v ./... 2>&1 | grep -E "^(--- (PASS|FAIL)|FAIL|ok|PASS)" | awk '{c[$1" "$2]++} END{for(k in c) print k, c[k]}'
go test -count=1 ./... 2>&1 | tail -3
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags '-s -w' -o olh-watchdog .
sha256sum olh-watchdog
