#!/bin/sh
# Builds the router binary. go build applies main/default.pgo by itself.
# Symbols stay in so perf can profile the running router.
# From Go 1.27 x/net/http2 wraps net/http's HTTP/2 unless built with
# http2legacy, and the XHTTP server needs the xnet fork's own server.
# usage: privata/build.sh [output]
set -eu
cd "$(dirname "$0")/.."
export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.27.2}"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags http2legacy -trimpath -buildvcs=false \
	-gcflags="all=-l=4" -ldflags "-buildid=" -o "${1:-xray}" ./main
