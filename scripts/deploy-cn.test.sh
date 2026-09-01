#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
remote_script="$(mktemp)"
trap 'rm -f "$remote_script"' EXIT

bash -n "$root/scripts/deploy-cn.sh"
awk '/^ssh .*REMOTE_DEPLOY.*/ {capture=1; next} capture && /^REMOTE_DEPLOY$/ {exit} capture {print}' \
	"$root/scripts/deploy-cn.sh" >"$remote_script"
bash -n "$remote_script"

output="$(
	DEPLOY_DRY_RUN=1 \
	REMOTE_HOST=deploy@example \
	REMOTE_DIR=/srv/life-ustc \
	"$root/scripts/deploy-cn.sh" 2>&1
)"

grep -F 'dry-run would deploy revision ' <<<"$output" >/dev/null
grep -F 'dry-run performs no SSH, SCP, Docker, source, or database changes' <<<"$output" >/dev/null

echo "deploy-cn dry-run test passed"
