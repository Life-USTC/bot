#!/usr/bin/env bash
# Cloud Agent bootstrap for the Life@USTC Bot (Go service).
# Idempotent: safe to re-run and to run against cached/snapshot state.
set -euo pipefail

# Pin the Go toolchain to match go.mod (requires >= go 1.25.x). The default
# Cloud Agent image ships an older Go, so install a matching toolchain into
# /usr/local/go and put it ahead of the system one on PATH (/usr/local/bin
# precedes /usr/bin). Detect by inspecting that exact install location so the
# check is not fooled by go.mod's toolchain auto-download.
GO_VERSION="1.25.12"
desired="go${GO_VERSION}"

installed=""
if [ -x /usr/local/go/bin/go ]; then
	installed="$(/usr/local/go/bin/go version 2>/dev/null | awk '{print $3}')"
fi

if [ "$installed" != "$desired" ]; then
	tarball="${desired}.linux-amd64.tar.gz"
	curl -fsSL -o "/tmp/${tarball}" "https://go.dev/dl/${tarball}"
	sudo rm -rf /usr/local/go
	sudo tar -C /usr/local -xzf "/tmp/${tarball}"
	rm -f "/tmp/${tarball}"
fi
sudo ln -sf /usr/local/go/bin/go /usr/local/bin/go
sudo ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt

hash -r
go version

# Repository dependencies (CGO is required for the SQLite driver; gcc is already
# present in the base image). Warm the build cache so the first agent is fast.
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
go mod download
go build ./...
