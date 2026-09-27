#!/bin/sh
set -eu
base=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
source=${CPA_SOURCE:-$(CDPATH= cd -- "$base/../../.." && pwd)}
case "$(uname -s)" in
  Darwin) ext=dylib ;;
  Linux) ext=so ;;
  *) printf '%s\n' 'This integration runner supports macOS and Linux.' >&2; exit 2 ;;
esac
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
library="$work/zcode-coding-plan.$ext"
(cd "$base/go" && go build -trimpath -ldflags='-s -w' -buildmode=c-shared -o "$library" .)
cp "$base/integration/go.mod" "$base"/integration/*_test.go "$work/"
(cd "$work" && go mod edit -replace "github.com/router-for-me/CLIProxyAPI/v7=$source" && go mod tidy)
(cd "$work" && CP_INTEGRATION_KEY=synthetic-key.synthetic-secret CP_INTEGRATION_DEVICE=synthetic-device CP_PLUGIN_LIBRARY="$library" go test -count=1 -timeout=180s -v ./...)
