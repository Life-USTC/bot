#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
server_dir="${LIFE_USTC_SERVER_DIR:-$root/../server}"
server_port="${DEV_E2E_SERVER_PORT:-3100}"
postgres_port="${DEV_E2E_POSTGRES_PORT:-55432}"
inspector_port="${DEV_E2E_INSPECTOR_PORT:-9311}"
compose_project="life-ustc-bot-e2e-$$"
database_name="life_ustc_bot_e2e"
database_url="postgresql://postgres:postgres@127.0.0.1:${postgres_port}/${database_name}"
server_origin="http://localhost:${server_port}"
mkdir -p "$root/.run"
run_dir="$(mktemp -d "$root/.run/dev-e2e.XXXXXX")"
worker_pid=""
completed=false

require_command() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "dev E2E requires $1" >&2
		exit 1
	fi
}

cleanup() {
	local exit_code=$?
	if [[ -n "$worker_pid" ]] && kill -0 "$worker_pid" >/dev/null 2>&1; then
		kill -TERM "$worker_pid" >/dev/null 2>&1 || true
		wait "$worker_pid" >/dev/null 2>&1 || true
	fi
	COMPOSE_PROJECT_NAME="$compose_project" \
		POSTGRES_PORT="$postgres_port" \
		POSTGRES_DB="$database_name" \
		docker compose -f "$server_dir/docker-compose.dev.yml" down --volumes >/dev/null 2>&1 || true
	if [[ "$completed" == true ]]; then
		rm -rf "$run_dir"
	else
		echo "dev E2E artifacts retained at $run_dir" >&2
	fi
	return "$exit_code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

for command in bun curl docker go; do
	require_command "$command"
done
if [[ ! -f "$server_dir/package.json" || ! -f "$server_dir/docker-compose.dev.yml" ]]; then
	echo "Life@USTC server checkout not found at $server_dir" >&2
	exit 1
fi
for port in "$server_port" "$postgres_port" "$inspector_port"; do
	if ! [[ "$port" =~ ^[0-9]+$ ]] || ((port < 1 || port > 65535)); then
		echo "invalid dev E2E port: $port" >&2
		exit 1
	fi
done

echo "[1/6] Starting isolated PostgreSQL on 127.0.0.1:${postgres_port}"
COMPOSE_PROJECT_NAME="$compose_project" \
	POSTGRES_PORT="$postgres_port" \
	POSTGRES_DB="$database_name" \
	docker compose -f "$server_dir/docker-compose.dev.yml" up -d --wait postgres

echo "[2/6] Preparing and seeding the local Life@USTC server"
(
	cd "$server_dir"
	bun install --frozen-lockfile
	DATABASE_URL="$database_url" bun run app:prepare
	DATABASE_URL="$database_url" bun run db:migrate:deploy
	DATABASE_URL="$database_url" ALLOW_DATABASE_SEED=true bunx prisma db seed
	DATABASE_URL="$database_url" bun run build
) >"$run_dir/server-prepare.log" 2>&1

echo "[3/6] Starting the real local Worker at ${server_origin}"
(
	cd "$server_dir"
	DATABASE_URL="$database_url" \
	CLOUDFLARE_HYPERDRIVE_LOCAL_CONNECTION_STRING_HYPERDRIVE="$database_url" \
	CLOUDFLARE_HYPERDRIVE_LOCAL_CONNECTION_STRING_HYPERDRIVE_AUTH="$database_url" \
	CLOUDFLARE_HYPERDRIVE_LOCAL_CONNECTION_STRING_HYPERDRIVE_MAINTENANCE="$database_url" \
	E2E_PORT="$server_port" \
	E2E_INSPECTOR_PORT="$inspector_port" \
	E2E_APP_PUBLIC_ORIGIN="$server_origin" \
	E2E_WORKER_ARTIFACT_DIR="$run_dir/server-worker" \
	bash tests/ci/e2e-worker-server.sh
) >"$run_dir/server-wrapper.log" 2>&1 &
worker_pid=$!

for _ in $(seq 1 300); do
	if curl --silent --show-error --fail --max-time 2 --noproxy '*' \
		"http://127.0.0.1:${server_port}/api/health" >/dev/null 2>&1; then
		break
	fi
	if ! kill -0 "$worker_pid" >/dev/null 2>&1; then
		echo "local Life@USTC server stopped during startup" >&2
		exit 1
	fi
	sleep 1
done
curl --silent --show-error --fail --max-time 2 --noproxy '*' \
	"http://127.0.0.1:${server_port}/api/health" >/dev/null

echo "[4/6] Building the Bot and protocol E2E runner"
go build -buildvcs=false -o "$run_dir/life-ustc-bot" ./cmd/life-ustc-bot
go build -buildvcs=false -o "$run_dir/life-ustc-bot-dev-e2e" ./cmd/life-ustc-bot-dev-e2e

echo "[5/6] Running private resume/iCalendar and shared-chat routing E2E"
"$run_dir/life-ustc-bot-dev-e2e" \
	-bot "$run_dir/life-ustc-bot" \
	-server "$server_origin" \
	-run-dir "$run_dir"

echo "[6/6] Local dev E2E passed"
completed=true
