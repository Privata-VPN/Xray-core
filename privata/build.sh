#!/bin/sh
# Builds the router binary. go build applies main/default.pgo by itself.
# Symbols stay in so perf can profile the running router.
# usage: privata/build.sh [output]
set -eu
cd "$(dirname "$0")/.."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
	-gcflags="all=-l=4" -ldflags "-buildid=" -o "${1:-xray}" ./main
