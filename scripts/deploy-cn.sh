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

ssh "$REMOTE_HOST" "mkdir -p '$REMOTE_DIR/src' '$REMOTE_DIR/data' && chmod 700 '$REMOTE_DIR'"

tar \
	--exclude='./.git' \
	--exclude='./.run' \
	--exclude='./.env' \
	--exclude='./life-ustc-bot' \
	--exclude='./*.log' \
	--exclude='./*.pid' \
	-C "$ROOT" \
	-czf - . | ssh "$REMOTE_HOST" "rm -rf '$REMOTE_DIR/src' && mkdir -p '$REMOTE_DIR/src' && tar -xzf - -C '$REMOTE_DIR/src'"

scp -q "$ROOT/$ENV_FILE" "$REMOTE_HOST:$REMOTE_DIR/.env"
ssh "$REMOTE_HOST" "chmod 600 '$REMOTE_DIR/.env'"

ssh "$REMOTE_HOST" "cd '$REMOTE_DIR' && docker compose up -d --build '$SERVICE' && docker compose ps '$SERVICE'"
