#!/usr/bin/env bash
set -euo pipefail

REMOTE_HOST="${REMOTE_HOST:-cn}"
REMOTE_DIR="${REMOTE_DIR:-/srv/docker/life-ustc-bot}"
SERVICE="${SERVICE:-bot}"
ENV_FILE="${ENV_FILE:-.env}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [[ ! -f "$ROOT/$ENV_FILE" ]]; then
	echo "missing env file: $ROOT/$ENV_FILE" >&2
	exit 1
fi

if [[ -n "$(git -C "$ROOT" status --porcelain --untracked-files=all)" ]]; then
	echo "refusing to deploy a dirty git checkout; commit or stash changes first" >&2
	git -C "$ROOT" status --short --untracked-files=all >&2
	exit 1
fi

REVISION="$(git -C "$ROOT" rev-parse HEAD)"

ssh "$REMOTE_HOST" "mkdir -p '$REMOTE_DIR/data' && chmod 700 '$REMOTE_DIR'"

git -C "$ROOT" archive --format=tar.gz --prefix=src/ "$REVISION" \
	| ssh "$REMOTE_HOST" "cd '$REMOTE_DIR' && rm -rf src && tar -xzf - && printf '%s\n' '$REVISION' > .deploy-revision"

scp -q "$ROOT/$ENV_FILE" "$REMOTE_HOST:$REMOTE_DIR/.env"
ssh "$REMOTE_HOST" "sed -i '/^BOT_BUILD_VERSION=/d' '$REMOTE_DIR/.env' && printf '%s\n' 'BOT_BUILD_VERSION=$REVISION' >> '$REMOTE_DIR/.env' && chmod 600 '$REMOTE_DIR/.env'"

ssh "$REMOTE_HOST" "cd '$REMOTE_DIR' && docker compose up -d --build '$SERVICE' && docker compose ps '$SERVICE'"
