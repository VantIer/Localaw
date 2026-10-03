#!/usr/bin/env bash
# Build Localaw for every supported target. Fully static, CGO disabled,
# zero external runtime dependencies. Requires Go 1.22+.
set -euo pipefail
cd "$(dirname "$0")/.."

export CGO_ENABLED=0
TAGS="netgo,osusergo"
LDFLAGS="-s -w"
mkdir -p dist

build() {
    local goos="$1" goarch="$2" ext="${3:-}"
    echo ">> localaw ${goos}/${goarch}"
    GOOS="$goos" GOARCH="$goarch" go build -trimpath -tags "$TAGS" -ldflags "$LDFLAGS" \
        -o "dist/localaw_${goos}_${goarch}${ext}" ./cmd/localaw
}

build windows amd64 .exe
build windows 386   .exe
build linux   amd64
build linux   386
build linux   arm64
