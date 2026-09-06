#!/usr/bin/env bash
set -euo pipefail

# Deploy one committed revision to the remote Compose project. The remote
# transaction deliberately keeps its staging and rollback directories so that
# an operator can inspect the exact source, image, database backup, and
# diagnostics for each deployment.

REMOTE_HOST="${REMOTE_HOST:?set REMOTE_HOST to the SSH host}"
REMOTE_DIR="${REMOTE_DIR:?set REMOTE_DIR to the remote deploy directory}"
SERVICE="${SERVICE:-bot}"
ENV_FILE="${ENV_FILE:-.env}"
DRY_RUN="${DEPLOY_DRY_RUN:-0}"
HEALTH_TIMEOUT="${DEPLOY_HEALTH_TIMEOUT:-180}"
IMAGE_REPOSITORY="${DEPLOY_IMAGE_REPOSITORY:-life-ustc-bot}"
RENDERD_IMAGE_REPOSITORY="${DEPLOY_RENDERD_IMAGE_REPOSITORY:-life-ustc-renderd}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

die() {
	echo "deploy-cn: $*" >&2
	exit 1
}

require_command() {
	command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

validate_remote_path() {
	local path="$1"
	[[ "$path" == /* && "$path" != "/" && "$path" != */ ]] || die "REMOTE_DIR must be a non-root absolute path without a trailing slash"
	[[ "$path" != *$'\n'* && "$path" != *$'\r'* ]] || die "REMOTE_DIR contains a newline"
	local relative_path="${path#/}"
	[[ "$relative_path" != *'//'* ]] || die "REMOTE_DIR contains an empty path component"
	local component
	local -a components
	IFS=/ read -r -a components <<<"$relative_path"
	for component in "${components[@]}"; do
		[[ -n "$component" && "$component" != "." && "$component" != ".." ]] || die "REMOTE_DIR contains an empty or traversal component"
		[[ "$component" =~ ^[A-Za-z0-9._-]+$ ]] || die "REMOTE_DIR contains an unsupported character"
	done
}

validate_remote_path "$REMOTE_DIR"
[[ "$REMOTE_HOST" != *[[:space:]]* && "$REMOTE_HOST" != *$'\n'* ]] || die "REMOTE_HOST contains whitespace"
[[ "$SERVICE" == bot ]] || die "SERVICE must be bot"
[[ "$ENV_FILE" =~ ^[A-Za-z0-9._-]+$ ]] || die "ENV_FILE must be a file name in the checkout"
[[ "$DRY_RUN" == 0 || "$DRY_RUN" == 1 ]] || die "DEPLOY_DRY_RUN must be 0 or 1"
[[ "$HEALTH_TIMEOUT" =~ ^[0-9]+$ ]] || die "DEPLOY_HEALTH_TIMEOUT must be a number of seconds"
(( HEALTH_TIMEOUT >= 1 && HEALTH_TIMEOUT <= 3600 )) || die "DEPLOY_HEALTH_TIMEOUT must be between 1 and 3600 seconds"
[[ "$IMAGE_REPOSITORY" =~ ^[a-z0-9]+([._/-][a-z0-9]+)*$ ]] || die "DEPLOY_IMAGE_REPOSITORY contains unsupported characters"
[[ "$RENDERD_IMAGE_REPOSITORY" =~ ^[a-z0-9]+([._/-][a-z0-9]+)*$ ]] || die "DEPLOY_RENDERD_IMAGE_REPOSITORY contains unsupported characters"
[[ "$IMAGE_REPOSITORY" != "$RENDERD_IMAGE_REPOSITORY" ]] || die "bot and renderd image repositories must differ"

require_command git
if [[ "$DRY_RUN" == 0 ]]; then
	[[ -f "$ROOT/$ENV_FILE" ]] || die "missing env file: $ROOT/$ENV_FILE"
	[[ ! -L "$ROOT/$ENV_FILE" ]] || die "env file must not be a symlink: $ROOT/$ENV_FILE"
	[[ -f "$ROOT/compose.yaml" && ! -L "$ROOT/compose.yaml" ]] || die "compose.yaml must be a regular file"
fi

if [[ -n "$(git -C "$ROOT" status --porcelain --untracked-files=all)" ]]; then
	if [[ "$DRY_RUN" == 0 ]]; then
		echo "refusing to deploy a dirty git checkout; commit or stash changes first" >&2
		git -C "$ROOT" status --short --untracked-files=all >&2
		exit 1
	fi
	echo "deploy-cn: dry-run from a dirty checkout; no remote changes will be made" >&2
fi

REVISION="$(git -C "$ROOT" rev-parse HEAD)"
[[ "$REVISION" =~ ^[0-9a-f]{40}$ ]] || die "git revision is not a 40-character lowercase SHA"

if [[ "$DRY_RUN" == 1 ]]; then
	echo "deploy-cn: dry-run would deploy revision $REVISION to $REMOTE_HOST:$REMOTE_DIR (service $SERVICE)"
	echo "deploy-cn: dry-run performs no SSH, SCP, Docker, source, or database changes"
	exit 0
fi

require_command ssh
require_command scp

quote_remote_arg() {
	local value="$1"
	printf "'%s'" "${value//\'/\'\\\'\'}"
}

DEPLOY_ID="$(date -u +%Y%m%dT%H%M%SZ)-$REVISION-$$"
REMOTE_STAGE="$REMOTE_DIR/.deploy-staging/$DEPLOY_ID"
REMOTE_ROLLBACK="$REMOTE_DIR/.deploy-rollback/$DEPLOY_ID"

REMOTE_ROOT_ARG="$(quote_remote_arg "$REMOTE_DIR")"
REMOTE_STAGE_ARG="$(quote_remote_arg "$REMOTE_STAGE")"
REMOTE_ROLLBACK_ARG="$(quote_remote_arg "$REMOTE_ROLLBACK")"

# Prepare only explicit, revision-scoped directories. In particular, the
# currently running src directory and data directory are not touched here.
ssh "$REMOTE_HOST" "bash -s -- $REMOTE_ROOT_ARG $REMOTE_STAGE_ARG $REMOTE_ROLLBACK_ARG" <<'REMOTE_PREPARE'
set -euo pipefail
root="$1"
stage="$2"
rollback="$3"

command -v tar >/dev/null 2>&1 || { echo "remote host is missing tar" >&2; exit 1; }
[[ "$root" == /* && "$root" != "/" ]] || exit 1
[[ "$stage" == "$root/.deploy-staging/"* ]] || exit 1
[[ "$rollback" == "$root/.deploy-rollback/"* ]] || exit 1
[[ ! -L "$root" ]] || { echo "remote deploy directory is a symlink" >&2; exit 1; }
mkdir -p "$root" "$root/data" "$root/.deploy-staging" "$root/.deploy-rollback"
[[ ! -L "$root/data" && ! -L "$root/.deploy-staging" && ! -L "$root/.deploy-rollback" ]] || { echo "remote deploy path contains a symlink" >&2; exit 1; }
[[ ! -e "$stage" && ! -L "$stage" && ! -e "$rollback" && ! -L "$rollback" ]] || { echo "remote deployment id already exists" >&2; exit 1; }
mkdir "$stage" "$rollback"
mkdir "$stage/src" "$stage/diagnostics"
chown 10001:10001 "$root/data"
chmod 700 "$root" "$root/data" "$root/.deploy-staging" "$root/.deploy-rollback" "$stage" "$stage/diagnostics" "$rollback"
REMOTE_PREPARE

# The archive contains exactly REVISION, including only tracked files. It is
# extracted into the unique stage while the previous service keeps running.
git -C "$ROOT" archive --format=tar.gz --prefix=src/ "$REVISION" \
	| ssh "$REMOTE_HOST" "tar -xzf - -C $REMOTE_STAGE_ARG"

scp -q "$ROOT/$ENV_FILE" "$REMOTE_HOST:$REMOTE_STAGE/.env"
scp -q "$ROOT/compose.yaml" "$REMOTE_HOST:$REMOTE_STAGE/compose.yaml"

REMOTE_REVISION_ARG="$(quote_remote_arg "$REVISION")"
REMOTE_SERVICE_ARG="$(quote_remote_arg "$SERVICE")"
REMOTE_TIMEOUT_ARG="$(quote_remote_arg "$HEALTH_TIMEOUT")"
REMOTE_IMAGE_REPOSITORY_ARG="$(quote_remote_arg "$IMAGE_REPOSITORY")"
REMOTE_RENDERD_IMAGE_REPOSITORY_ARG="$(quote_remote_arg "$RENDERD_IMAGE_REPOSITORY")"
REMOTE_DEPLOY_ID_ARG="$(quote_remote_arg "$DEPLOY_ID")"

# All remote command output is retained under a mode-700 stage directory. FD
# 3 is the only channel used for concise, non-secret operator status.
ssh "$REMOTE_HOST" "bash -s -- $REMOTE_ROOT_ARG $REMOTE_STAGE_ARG $REMOTE_ROLLBACK_ARG $REMOTE_REVISION_ARG $REMOTE_SERVICE_ARG $REMOTE_TIMEOUT_ARG $REMOTE_IMAGE_REPOSITORY_ARG $REMOTE_RENDERD_IMAGE_REPOSITORY_ARG $REMOTE_DEPLOY_ID_ARG" <<'REMOTE_DEPLOY'
#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

ROOT="$1"
STAGE="$2"
ROLLBACK="$3"
REVISION="$4"
SERVICE="$5"
RENDERD_SERVICE="renderd"
HEALTH_TIMEOUT="$6"
IMAGE_REPOSITORY="$7"
RENDERD_IMAGE_REPOSITORY="$8"
DEPLOY_ID="$9"

exec 3>&1
mkdir -p "$STAGE/diagnostics"
chmod 700 "$STAGE" "$STAGE/diagnostics" "$ROLLBACK"
: >"$STAGE/deploy.log"
chmod 600 "$STAGE/deploy.log"
exec >"$STAGE/deploy.log" 2>&1

log() {
	printf '%s\n' "$*" >&3
}

fail() {
	echo "$*" >&2
	printf 'deployment step failed: %s\n' "$*" >&3
	return 1
}

assert_no_symlink_components() {
	local path="$1"
	local current="/"
	local component
	local -a components
	IFS=/ read -r -a components <<<"${path#/}"
	for component in "${components[@]}"; do
		[[ -n "$component" ]] || fail "path contains an empty component"
		current="$current$component"
		[[ ! -L "$current" ]] || fail "path contains a symlink: $current"
		current="$current/"
	done
}

is_container_id() {
	local value="$1"
	[[ "$value" != *$'\n'* && "$value" =~ ^[0-9a-f]{12,64}$ ]]
}

[[ "$ROOT" == /* && "$ROOT" != "/" ]] || fail "invalid remote root"
[[ "$STAGE" == "$ROOT/.deploy-staging/"* ]] || fail "invalid remote stage"
[[ "$ROLLBACK" == "$ROOT/.deploy-rollback/"* ]] || fail "invalid remote rollback directory"
[[ "$REVISION" =~ ^[0-9a-f]{40}$ ]] || fail "invalid revision"
[[ "$SERVICE" == bot ]] || fail "service must be bot"
[[ "$HEALTH_TIMEOUT" =~ ^[0-9]+$ ]] || fail "invalid health timeout"
(( HEALTH_TIMEOUT >= 1 && HEALTH_TIMEOUT <= 3600 )) || fail "health timeout out of range"
[[ "$IMAGE_REPOSITORY" =~ ^[a-z0-9]+([._/-][a-z0-9]+)*$ ]] || fail "invalid image repository"
[[ "$RENDERD_IMAGE_REPOSITORY" =~ ^[a-z0-9]+([._/-][a-z0-9]+)*$ ]] || fail "invalid renderd image repository"
[[ "$IMAGE_REPOSITORY" != "$RENDERD_IMAGE_REPOSITORY" ]] || fail "bot and renderd image repositories must differ"
[[ "$DEPLOY_ID" =~ ^[0-9A-Za-z_.-]+$ ]] || fail "invalid deployment id"

DATA_DIR="$ROOT/data"
ACTIVE_COMPOSE="$ROOT/compose.yaml"
ACTIVE_ENV="$ROOT/.env"
NEW_IMAGE="$IMAGE_REPOSITORY:$REVISION"
NEW_RENDERD_IMAGE="$RENDERD_IMAGE_REPOSITORY:$REVISION"
PREVIOUS_IMAGE="$IMAGE_REPOSITORY:rollback-$DEPLOY_ID"
PREVIOUS_RENDERD_IMAGE="$RENDERD_IMAGE_REPOSITORY:rollback-$DEPLOY_ID"
STAGE_IMAGE_FILE="$STAGE/image.yaml"
ROLLBACK_IMAGE_FILE="$ROLLBACK/image.yaml"
DIAGNOSTIC_DIR="$STAGE/diagnostics"
FAILED_ACTIVE_DIR="$STAGE/failed-active"
MIGRATION_DATA_DIR="$STAGE/migration-data"
MIGRATION_LOG="$STAGE/migration.log"
LOCK_FILE="$ROOT/.deploy.lock"
REVISION_FILE="$ROOT/.deploy-revision"
REVISION_TEMP_FILE="$ROOT/.deploy-revision.tmp"
STATE_FILE="$ROOT/.deploy-state"
STATE_TEMP_FILE="$ROOT/.deploy-state.tmp"
STOP_WAIT_ATTEMPTS=15

PREVIOUS_CONTAINER_ID=""
PREVIOUS_IMAGE_ID=""
PREVIOUS_IMAGE_REFERENCE=""
PREVIOUS_RENDERD_CONTAINER_ID=""
PREVIOUS_RENDERD_IMAGE_ID=""
PREVIOUS_RENDERD_IMAGE_REFERENCE=""
PREVIOUS_RENDERD_WAS_RUNNING=0
PREVIOUS_HAS_RENDERD=0
PREVIOUS_REVISION="unknown"
PREVIOUS_REVISION_PRESENT=0
PREVIOUS_STATE_PRESENT=0
PREVIOUS_WAS_RUNNING=0
PREVIOUS_DB_PRESENT=0
DB_STATE_KNOWN=0
BACKUP_CREATED=0
SERVICE_STOP_ATTEMPTED=0
SERVICE_QUIESCED=0
PROMOTION_STARTED=0
OLD_SRC_MOVED=0
NEW_SRC_MOVED=0
OLD_COMPOSE_MOVED=0
NEW_COMPOSE_MOVED=0
OLD_ENV_MOVED=0
NEW_ENV_MOVED=0
NEW_REVISION_WRITTEN=0
REVISION_WRITE_STARTED=0
STATE_WRITE_STARTED=0
NEW_CONTAINER_ID=""
NEW_IMAGE_ID=""
NEW_RENDERD_IMAGE_ID=""
NEW_RENDERD_CONTAINER_ID=""
STARTUP_ATTEMPTED=0
ROLLBACK_CONTAINER_ID=""
ROLLBACK_RENDERD_CONTAINER_ID=""
MIGRATION_GATE_COMPLETE=0
ROLLBACK_OK=0
HANDLING_ERROR=0
DEPLOY_SUCCEEDED=0

preflight_error() {
	local rc="$1"
	log "deployment failed during remote preflight; diagnostics retained at $DIAGNOSTIC_DIR"
	exit "$rc"
}
trap 'preflight_error "$?"' ERR

if [[ -f "$REVISION_FILE" && ! -L "$REVISION_FILE" ]]; then
	PREVIOUS_REVISION_PRESENT=1
	PREVIOUS_REVISION="$(tr -d '\r\n' <"$REVISION_FILE")"
	[[ "$PREVIOUS_REVISION" =~ ^[0-9a-f]{40}$ ]] || PREVIOUS_REVISION="unknown"
fi

for command in docker python3 flock; do
	command -v "$command" >/dev/null 2>&1 || fail "missing remote command: $command"
done
python3 -c 'import sqlite3' >/dev/null 2>&1 || fail "remote python3 has no sqlite3 module"
[[ -d "$ROOT" && ! -L "$ROOT" && ! -L "$LOCK_FILE" ]] || fail "remote deploy root or lock path is a symlink"
exec 9>"$LOCK_FILE"
flock -n 9 || fail "another deployment is already running for this directory"

[[ -d "$DATA_DIR" && ! -L "$DATA_DIR" ]] || fail "remote data directory is missing or a symlink"
[[ -f "$STAGE/.env" && ! -L "$STAGE/.env" ]] || fail "staged env file is missing or a symlink"
[[ -f "$STAGE/compose.yaml" && ! -L "$STAGE/compose.yaml" ]] || fail "staged compose file is missing or a symlink"
[[ -d "$STAGE/src" && ! -L "$STAGE/src" ]] || fail "staged source directory is missing or a symlink"
if [[ -e "$ACTIVE_COMPOSE" || -L "$ACTIVE_COMPOSE" ]]; then
	[[ -f "$ACTIVE_COMPOSE" && ! -L "$ACTIVE_COMPOSE" ]] || fail "active compose file is not a regular file"
fi
if [[ -e "$ACTIVE_ENV" || -L "$ACTIVE_ENV" ]]; then
	[[ -f "$ACTIVE_ENV" && ! -L "$ACTIVE_ENV" ]] || fail "active env file is not a regular file"
fi
if [[ -e "$REVISION_FILE" || -L "$REVISION_FILE" ]]; then
	[[ -f "$REVISION_FILE" && ! -L "$REVISION_FILE" ]] || fail "active revision marker is not a regular file"
fi

if [[ -e "$STATE_FILE" || -L "$STATE_FILE" ]]; then
	[[ -f "$STATE_FILE" && ! -L "$STATE_FILE" ]] || fail "active deployment state is not a regular file"
	PREVIOUS_STATE_PRESENT=1
fi
if [[ -e "$STATE_TEMP_FILE" || -L "$STATE_TEMP_FILE" ]]; then
	fail "stale deployment state temporary file exists"
fi
if [[ -e "$REVISION_TEMP_FILE" || -L "$REVISION_TEMP_FILE" ]]; then
	fail "stale deployment revision temporary file exists"
fi

chmod 600 "$STAGE/.env"
if [[ "$(grep -Ec '^(export[[:space:]]+)?BOT_BUILD_VERSION=' "$STAGE/.env" || true)" -ne 0 ]]; then
	# A staged copy may have inherited an old value from the checkout. Remove
	# every old entry before writing the exact archived revision.
	sed -i -E '/^(export[[:space:]]+)?BOT_BUILD_VERSION=/d' "$STAGE/.env"
fi
printf 'BOT_BUILD_VERSION=%s\n' "$REVISION" >>"$STAGE/.env"

parse_db_container_path() {
python3 - "$1" <<'PY'
import sys

path = "/data/life-ustc-bot.db"
seen = False
for raw in open(sys.argv[1], encoding="utf-8"):
    line = raw.strip()
    if not line or line.startswith("#"):
        continue
    key, separator, value = line.partition("=")
    normalized_key = key.strip()
    if normalized_key == "export BOT_DB_PATH":
        normalized_key = "BOT_DB_PATH"
    if separator and normalized_key == "BOT_DB_PATH":
        if seen:
            raise SystemExit("BOT_DB_PATH appears more than once")
        seen = True
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in "'\"":
            value = value[1:-1]
        path = value
print(path)
PY
}

if ! DB_CONTAINER_PATH="$(parse_db_container_path "$STAGE/.env")"; then
	fail "could not parse BOT_DB_PATH from staged env file"
fi
[[ "$DB_CONTAINER_PATH" == /data/* ]] || fail "BOT_DB_PATH must be under /data for safe backup"
DB_RELATIVE_PATH="${DB_CONTAINER_PATH#/data/}"
[[ -n "$DB_RELATIVE_PATH" && "$DB_RELATIVE_PATH" != /* && "$DB_RELATIVE_PATH" != *'..'* && "$DB_RELATIVE_PATH" != *'$'* ]] || fail "BOT_DB_PATH contains an unsafe path"
DB_PATH="$DATA_DIR/$DB_RELATIVE_PATH"
PREVIOUS_DB_CONTAINER_PATH="/data/life-ustc-bot.db"
if [[ -f "$ACTIVE_ENV" && ! -L "$ACTIVE_ENV" ]]; then
	if ! PREVIOUS_DB_CONTAINER_PATH="$(parse_db_container_path "$ACTIVE_ENV")"; then
		fail "could not parse BOT_DB_PATH from active env file"
	fi
fi
[[ "$PREVIOUS_DB_CONTAINER_PATH" == "$DB_CONTAINER_PATH" ]] || fail "BOT_DB_PATH changed; refusing an implicit database migration"
DB_PARENT="$(dirname "$DB_PATH")"
assert_no_symlink_components "$DB_PARENT"
mkdir -p "$DB_PARENT"
assert_no_symlink_components "$DB_PARENT"
[[ ! -L "$DB_PARENT" && ! -L "$DB_PATH" && ! -L "$DB_PATH-wal" && ! -L "$DB_PATH-shm" ]] || fail "database path contains a symlink"

# Supply immutable image references for both services in this revision-scoped
# overlay. Keeping the sidecar explicit means a same-revision rebuild cannot
# overwrite the image that a rollback needs.
cat >"$STAGE_IMAGE_FILE" <<EOF
services:
  bot:
    image: $NEW_IMAGE
  renderd:
    image: $NEW_RENDERD_IMAGE
EOF
chmod 600 "$STAGE_IMAGE_FILE"

compose_stage() {
	docker compose --project-directory "$STAGE" --env-file "$STAGE/.env" \
		-f "$STAGE/compose.yaml" -f "$STAGE_IMAGE_FILE" "$@"
}

compose_stage_base() {
	docker compose --project-directory "$STAGE" --env-file "$STAGE/.env" \
		-f "$STAGE/compose.yaml" "$@"
}

compose_existing() {
	local compose_file="$ACTIVE_COMPOSE"
	local project_directory="$ROOT"
	if [[ ! -f "$compose_file" ]]; then
		compose_file="$STAGE/compose.yaml"
		project_directory="$STAGE"
	fi
	if [[ -f "$ACTIVE_ENV" ]]; then
		docker compose --project-directory "$project_directory" --env-file "$ACTIVE_ENV" \
			-f "$compose_file" "$@"
	else
		docker compose --project-directory "$project_directory" -f "$compose_file" "$@"
	fi
}

compose_active_new() {
	docker compose --project-directory "$ROOT" --env-file "$ACTIVE_ENV" \
		-f "$ACTIVE_COMPOSE" -f "$STAGE_IMAGE_FILE" "$@"
}

compose_rollback() {
	local compose_file="$ACTIVE_COMPOSE"
	local env_file="$ACTIVE_ENV"
	if [[ ! -f "$compose_file" || -L "$compose_file" ]]; then
		compose_file="$ROLLBACK/compose.yaml"
	fi
	if [[ -f "$env_file" && ! -L "$env_file" ]]; then
		docker compose --project-directory "$ROOT" --env-file "$env_file" \
			-f "$compose_file" -f "$ROLLBACK_IMAGE_FILE" "$@"
	else
		docker compose --project-directory "$ROOT" \
			-f "$compose_file" -f "$ROLLBACK_IMAGE_FILE" "$@"
	fi
}

preserve_image_tag() {
	local image_id="$1"
	local rollback_tag="$2"
	local tagged_id
	docker image inspect "$image_id" >/dev/null || return 1
	docker tag "$image_id" "$rollback_tag" || return 1
	tagged_id="$(docker image inspect --format '{{.Id}}' "$rollback_tag")" || return 1
	[[ "$tagged_id" == "$image_id" ]]
}

collect_diagnostics() {
	local target="$DIAGNOSTIC_DIR/summary.txt"
	mkdir -p "$DIAGNOSTIC_DIR"
	chmod 700 "$DIAGNOSTIC_DIR"
	{
		printf 'revision=%s\n' "$REVISION"
		printf 'previous_revision=%s\n' "$PREVIOUS_REVISION"
		printf 'new_image=%s\n' "$NEW_IMAGE"
		printf 'new_image_id=%s\n' "${NEW_IMAGE_ID:-unknown}"
		printf 'new_renderd_image=%s\n' "$NEW_RENDERD_IMAGE"
		printf 'new_renderd_image_id=%s\n' "${NEW_RENDERD_IMAGE_ID:-unknown}"
		printf 'database_path=%s\n' "$DB_PATH"
		printf 'service=%s\n' "$SERVICE"
		printf '\nactive compose status:\n'
		if [[ -f "$ACTIVE_COMPOSE" && -f "$ACTIVE_ENV" ]]; then
			compose_active_new ps --all "$SERVICE" || true
		elif [[ -f "$ACTIVE_COMPOSE" ]]; then
			docker compose --project-directory "$ROOT" -f "$ACTIVE_COMPOSE" ps --all "$SERVICE" || true
		fi
		if [[ -n "$NEW_CONTAINER_ID" ]]; then
			printf '\nnew container state:\n'
			docker inspect --format 'id={{.Id}} image={{.Image}} state={{.State.Status}} health={{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$NEW_CONTAINER_ID" || true
		fi
		if [[ -n "$NEW_RENDERD_CONTAINER_ID" ]]; then
			printf '\nnew renderd container state:\n'
			docker inspect --format 'id={{.Id}} image={{.Image}} state={{.State.Status}} health={{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$NEW_RENDERD_CONTAINER_ID" || true
		fi
	} >"$target" 2>&1 || true
	chmod 600 "$target" 2>/dev/null || true
	if [[ -f "$ACTIVE_COMPOSE" && -f "$ACTIVE_ENV" ]]; then
		compose_active_new logs --no-color --tail=200 "$SERVICE" >"$DIAGNOSTIC_DIR/service.log" 2>&1 || true
		chmod 600 "$DIAGNOSTIC_DIR/service.log" 2>/dev/null || true
		compose_active_new logs --no-color --tail=200 "$RENDERD_SERVICE" >"$DIAGNOSTIC_DIR/renderd.log" 2>&1 || true
		chmod 600 "$DIAGNOSTIC_DIR/renderd.log" 2>/dev/null || true
	fi
	for log_name in deploy.log config.log build.log stop.log start.log migration.log rollback-start.log; do
		if [[ -f "$STAGE/$log_name" ]]; then
			cp -- "$STAGE/$log_name" "$DIAGNOSTIC_DIR/$log_name" || true
			chmod 600 "$DIAGNOSTIC_DIR/$log_name" 2>/dev/null || true
		fi
	done
}

wait_for_healthy() {
	local mode="$1"
	local service="${2:-$SERVICE}"
	local started
	local now
	local container_id
	local state
	local status
	started="$(date +%s)"
	while :; do
		if [[ "$mode" == active ]]; then
			container_id="$(compose_active_new ps -q "$service" 2>/dev/null || true)"
		else
			container_id="$(compose_rollback ps -q "$service" 2>/dev/null || true)"
		fi
		if [[ "$container_id" =~ ^[0-9a-f]{12,64}$ ]]; then
			state="$(docker inspect --format '{{.State.Status}}' "$container_id" 2>/dev/null || true)"
			status="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}no-health{{end}}' "$container_id" 2>/dev/null || true)"
			case "$status" in
				healthy)
					if [[ "$state" == running ]]; then
						if [[ "$mode" == active ]]; then
							if [[ "$service" == "$RENDERD_SERVICE" ]]; then
								NEW_RENDERD_CONTAINER_ID="$container_id"
							else
								NEW_CONTAINER_ID="$container_id"
							fi
						else
							if [[ "$service" == "$RENDERD_SERVICE" ]]; then
								ROLLBACK_RENDERD_CONTAINER_ID="$container_id"
							else
								ROLLBACK_CONTAINER_ID="$container_id"
							fi
						fi
						return 0
					fi
					;;
				unhealthy|no-health)
					return 1
					;;
			esac
			if [[ "$state" == exited || "$state" == dead || "$state" == created ]]; then
				return 1
			fi
		fi
		now="$(date +%s)"
		(( now - started < HEALTH_TIMEOUT )) || return 1
		sleep 2
	done
}

wait_for_stopped() {
	local container_id="$1"
	local description="$2"
	local state
	local attempt
	is_container_id "$container_id" || return 1
	for ((attempt = 0; attempt < STOP_WAIT_ATTEMPTS; attempt++)); do
		state="$(docker inspect --format '{{.State.Running}}' "$container_id" 2>/dev/null)" || return 1
		if [[ "$state" != true ]]; then
			return 0
		fi
		sleep 1
	done
	log "$description did not stop within ${STOP_WAIT_ATTEMPTS}s"
	return 1
}

verify_container_image() {
	local container_id="$1"
	local expected_image_id="$2"
	local actual_image_id
	is_container_id "$container_id" || return 1
	[[ -n "$expected_image_id" ]] || return 1
	actual_image_id="$(docker inspect --format '{{.Image}}' "$container_id" 2>/dev/null)" || return 1
	[[ "$actual_image_id" == "$expected_image_id" ]]
}

verify_image_tag() {
	local image_reference="$1"
	local expected_image_id="$2"
	local actual_image_id
	actual_image_id="$(docker image inspect --format '{{.Id}}' "$image_reference" 2>/dev/null)" || return 1
	[[ "$actual_image_id" == "$expected_image_id" ]]
}

start_rollback_service() {
	local service="$1"
	local expected_image_id="$2"
	local container_id
	local previous_id
	local rollback_image
	local rollback_image_id
	[[ "$expected_image_id" =~ ^sha256:[0-9a-f]{64}$ ]] || return 1
	if [[ "$service" == "$RENDERD_SERVICE" ]]; then
		previous_id="$PREVIOUS_RENDERD_CONTAINER_ID"
		rollback_image="$PREVIOUS_RENDERD_IMAGE"
	else
		previous_id="$PREVIOUS_CONTAINER_ID"
		rollback_image="$PREVIOUS_IMAGE"
	fi
	rollback_image_id="$(docker image inspect --format '{{.Id}}' "$rollback_image" 2>/dev/null)" || return 1
	[[ "$rollback_image_id" == "$expected_image_id" ]] || return 1
	if ! compose_rollback up -d --pull never --no-build --no-deps "$service" >>"$DIAGNOSTIC_DIR/rollback-start.log" 2>&1; then
		cleanup_rollback_service "$service" "$previous_id" || true
		return 1
	fi
	if ! wait_for_healthy rollback "$service"; then
		cleanup_rollback_service "$service" "$previous_id" || true
		return 1
	fi
	if [[ "$service" == "$RENDERD_SERVICE" ]]; then
		container_id="$ROLLBACK_RENDERD_CONTAINER_ID"
	else
		container_id="$ROLLBACK_CONTAINER_ID"
	fi
	if ! verify_container_image "$container_id" "$expected_image_id"; then
		cleanup_rollback_service "$service" "$previous_id" || true
		return 1
	fi
}

cleanup_rollback_service() {
	local service="$1"
	local previous_id="$2"
	local candidates
	local candidate
	local cleanup_rc=0
	if ! candidates="$(compose_rollback ps -aq "$service" 2>/dev/null)"; then
		return 1
	fi
	while IFS= read -r candidate; do
		[[ -z "$candidate" ]] && continue
		if ! is_container_id "$candidate"; then
			cleanup_rc=1
			continue
		fi
		# Compose may have reused the original container. It is part of the
		# captured previous state and must never be removed as failed-rollback
		# cleanup.
		[[ "$candidate" == "$previous_id" ]] && continue
		remove_new_container "$candidate" || cleanup_rc=1
	done <<<"$candidates"
	return "$cleanup_rc"
}

stop_rollback_service() {
	local service="$1"
	local container_id
	if [[ "$service" == "$RENDERD_SERVICE" ]]; then
		container_id="$ROLLBACK_RENDERD_CONTAINER_ID"
	else
		container_id="$ROLLBACK_CONTAINER_ID"
	fi
	is_container_id "$container_id" || return 1
	docker stop "$container_id" >>"$DIAGNOSTIC_DIR/rollback-start.log" 2>&1 || return 1
	wait_for_stopped "$container_id" "rollback $service" || return 1
}

remove_new_container() {
	local container_id="$1"
	local running
	is_container_id "$container_id" || return 1
	if ! running="$(docker inspect --format '{{.State.Running}}' "$container_id" 2>/dev/null)"; then
		return 1
	fi
	# A Compose operation can reuse a captured container, especially when the
	# previous service was stopped. Preserve that container, but make sure a
	# formerly stopped one is not accidentally left running.
	if [[ "$container_id" == "$PREVIOUS_CONTAINER_ID" || "$container_id" == "$PREVIOUS_RENDERD_CONTAINER_ID" ]]; then
		if [[ "$running" == true ]]; then
			docker stop "$container_id" >/dev/null 2>&1 || return 1
			wait_for_stopped "$container_id" "replacement container" || return 1
		fi
		return 0
	fi
	if [[ "$running" == true ]]; then
		docker stop "$container_id" >/dev/null 2>&1 || return 1
		wait_for_stopped "$container_id" "replacement container" || return 1
	fi
	# Treat an rm/inspect failure as an unresolved container, rather than
	# claiming rollback succeeded when the daemon may still have it running.
	docker rm "$container_id" >/dev/null 2>&1 || return 1
}

restore_database() {
	local failed_database_dir="$STAGE/failed-database"
	if (( DB_STATE_KNOWN == 0 )); then
		# The database was not inspected or modified yet. There is nothing to
		# restore if quiescing failed before the checkpoint.
		return 0
	fi
	if (( PREVIOUS_DB_PRESENT == 1 && BACKUP_CREATED == 0 )); then
		# The backup was not proven complete. Leave the original database in
		# place instead of risking an irreversible move during rollback; no
		# replacement service has started while backup creation is in progress.
		return 0
	fi
	mkdir -p "$failed_database_dir" || return 1
	chmod 700 "$failed_database_dir" || return 1
	for path in "$DB_PATH" "$DB_PATH-wal" "$DB_PATH-shm"; do
		if [[ -e "$path" || -L "$path" ]]; then
			[[ ! -L "$path" ]] || return 1
			local destination="$failed_database_dir/$(basename "$path")"
			[[ ! -e "$destination" && ! -L "$destination" ]] || return 1
			mv -- "$path" "$destination" || return 1
		fi
	done
	if (( PREVIOUS_DB_PRESENT == 1 )); then
		[[ -f "$ROLLBACK/database.sqlite" && ! -L "$ROLLBACK/database.sqlite" ]] || return 1
		cp -- "$ROLLBACK/database.sqlite" "$DB_PATH" || return 1
		chmod 600 "$DB_PATH" || return 1
		chown 10001:10001 "$DB_PATH" || return 1
	fi
}

quarantine_path() {
	local source_path="$1"
	local destination_path="$2"
	if [[ -e "$source_path" || -L "$source_path" ]]; then
		[[ ! -L "$source_path" ]] || return 1
		[[ ! -e "$destination_path" && ! -L "$destination_path" ]] || return 1
		mv -- "$source_path" "$destination_path" || return 1
	fi
}

quarantine_new_path() {
	local active_path="$1"
	local failed_path="$2"
	local new_moved="$3"
	local old_moved="$4"
	local old_backup="$5"
	(( new_moved == 1 )) || return 0
	# If the old move was interrupted before its destination appeared, the
	# active path is still the old path and must be left in place.
	if (( old_moved == 1 )) && [[ ! -e "$old_backup" && ! -L "$old_backup" ]]; then
		return 0
	fi
	quarantine_path "$active_path" "$failed_path"
}

restore_old_path() {
	local old_moved="$1"
	local old_backup="$2"
	local active_path="$3"
	(( old_moved == 1 )) || return 0
	if [[ -e "$old_backup" || -L "$old_backup" ]]; then
		[[ ! -L "$old_backup" ]] || return 1
		[[ ! -e "$active_path" && ! -L "$active_path" ]] || return 1
		mv -- "$old_backup" "$active_path" || return 1
		return 0
	fi
	# The flag is set before mv so an interrupted mv leaves the original active
	# path untouched. If neither side exists, the transaction is ambiguous.
	[[ -e "$active_path" && ! -L "$active_path" ]]
}

restore_promoted_files() {
	if ! mkdir -p "$FAILED_ACTIVE_DIR" || ! chmod 700 "$FAILED_ACTIVE_DIR"; then
		return 1
	fi

	if (( STATE_WRITE_STARTED == 1 )); then
		# State is replaced atomically from a temp file. Preserve both an
		# interrupted temp and a completed new state before restoring the snapshot.
		quarantine_path "$STATE_TEMP_FILE" "$FAILED_ACTIVE_DIR/.deploy-state.tmp" || return 1
		if [[ -e "$STATE_FILE" || -L "$STATE_FILE" ]]; then
			if (( PREVIOUS_STATE_PRESENT == 1 )) && [[ -f "$ROLLBACK/previous-state.snapshot" ]] && cmp -s "$STATE_FILE" "$ROLLBACK/previous-state.snapshot"; then
				:
			else
				quarantine_path "$STATE_FILE" "$FAILED_ACTIVE_DIR/.deploy-state" || return 1
			fi
		fi
		if (( PREVIOUS_STATE_PRESENT == 1 )); then
			[[ -f "$ROLLBACK/previous-state.snapshot" && ! -L "$ROLLBACK/previous-state.snapshot" ]] || return 1
			cp -- "$ROLLBACK/previous-state.snapshot" "$STATE_FILE" || return 1
			chmod 600 "$STATE_FILE" || return 1
		else
			[[ ! -e "$STATE_FILE" && ! -L "$STATE_FILE" ]] || return 1
		fi
	fi

	if (( REVISION_WRITE_STARTED == 1 )); then
		quarantine_path "$REVISION_TEMP_FILE" "$FAILED_ACTIVE_DIR/.deploy-revision.tmp" || return 1
		if [[ -e "$REVISION_FILE" || -L "$REVISION_FILE" ]]; then
			if (( PREVIOUS_REVISION_PRESENT == 1 )) && [[ -f "$ROLLBACK/.deploy-revision" ]] && cmp -s "$REVISION_FILE" "$ROLLBACK/.deploy-revision"; then
				:
			else
				quarantine_path "$REVISION_FILE" "$FAILED_ACTIVE_DIR/.deploy-revision" || return 1
			fi
		fi
		if (( PREVIOUS_REVISION_PRESENT == 1 )); then
			[[ -f "$ROLLBACK/.deploy-revision" && ! -L "$ROLLBACK/.deploy-revision" ]] || return 1
			cp -- "$ROLLBACK/.deploy-revision" "$REVISION_FILE" || return 1
			chmod 600 "$REVISION_FILE" || return 1
		else
			[[ ! -e "$REVISION_FILE" && ! -L "$REVISION_FILE" ]] || return 1
		fi
	fi

	quarantine_new_path "$ROOT/.env" "$FAILED_ACTIVE_DIR/.env" "$NEW_ENV_MOVED" "$OLD_ENV_MOVED" "$ROLLBACK/.env" || return 1
	quarantine_new_path "$ROOT/compose.yaml" "$FAILED_ACTIVE_DIR/compose.yaml" "$NEW_COMPOSE_MOVED" "$OLD_COMPOSE_MOVED" "$ROLLBACK/compose.yaml" || return 1
	quarantine_new_path "$ROOT/src" "$FAILED_ACTIVE_DIR/src" "$NEW_SRC_MOVED" "$OLD_SRC_MOVED" "$ROLLBACK/src" || return 1

	restore_old_path "$OLD_SRC_MOVED" "$ROLLBACK/src" "$ROOT/src" || return 1
	restore_old_path "$OLD_COMPOSE_MOVED" "$ROLLBACK/compose.yaml" "$ROOT/compose.yaml" || return 1
	restore_old_path "$OLD_ENV_MOVED" "$ROLLBACK/.env" "$ROOT/.env" || return 1
}

cleanup_active_containers() {
	local service="$1"
	local candidates
	local candidate
	local cleanup_rc=0
	if ! candidates="$(compose_active_new ps -aq "$service" 2>/dev/null)"; then
		return 1
	fi
	while IFS= read -r candidate; do
		[[ -z "$candidate" ]] && continue
		if ! is_container_id "$candidate"; then
			cleanup_rc=1
			continue
		fi
		if [[ "$service" == "$RENDERD_SERVICE" ]]; then
			[[ -n "$NEW_RENDERD_CONTAINER_ID" ]] || NEW_RENDERD_CONTAINER_ID="$candidate"
		else
			[[ -n "$NEW_CONTAINER_ID" ]] || NEW_CONTAINER_ID="$candidate"
		fi
		remove_new_container "$candidate" || cleanup_rc=1
	done <<<"$candidates"
	return "$cleanup_rc"
}

rollback_transaction() {
	local rollback_rc=0
	local service_rc
	local rollback_image_ready=1
	set +e
	# Compose may have created either service before the transaction failed. Find
	# and remove every replacement container while the promoted files are still
	# active so a failed deployment cannot leave a new renderd process behind.
	if (( STARTUP_ATTEMPTED == 1 )); then
		cleanup_active_containers "$SERVICE" || rollback_rc=1
		cleanup_active_containers "$RENDERD_SERVICE" || rollback_rc=1
	fi
	if (( SERVICE_QUIESCED == 1 )); then
		restore_database || rollback_rc=1
	fi
	if (( PROMOTION_STARTED == 1 )); then
		restore_promoted_files || rollback_rc=1
	fi
	if (( rollback_rc == 0 )) && { [[ -n "$PREVIOUS_CONTAINER_ID" ]] || [[ -n "$PREVIOUS_RENDERD_CONTAINER_ID" ]]; }; then
		if [[ ! -f "$ROLLBACK_IMAGE_FILE" ]]; then
			if ! {
				printf 'services:\n'
				if [[ -n "$PREVIOUS_CONTAINER_ID" ]]; then
					printf '  bot:\n    image: %s\n' "$PREVIOUS_IMAGE"
				fi
				if [[ -n "$PREVIOUS_RENDERD_CONTAINER_ID" ]]; then
					printf '  %s:\n    image: %s\n' "$RENDERD_SERVICE" "$PREVIOUS_RENDERD_IMAGE"
				fi
			} >"$ROLLBACK_IMAGE_FILE"; then
				rollback_image_ready=0
			elif ! chmod 600 "$ROLLBACK_IMAGE_FILE"; then
				rollback_image_ready=0
			fi
		fi
		if (( rollback_image_ready == 1 )); then
			: >"$DIAGNOSTIC_DIR/rollback-start.log" || rollback_image_ready=0
		fi
		(( rollback_image_ready == 1 )) || rollback_rc=1
		# Restore every captured service. A service that was stopped before the
		# deployment is started long enough to verify its image, then stopped again
		# so the prior running state is preserved.
		if (( rollback_image_ready == 1 )) && [[ -n "$PREVIOUS_RENDERD_CONTAINER_ID" ]]; then
			service_rc=0
			start_rollback_service "$RENDERD_SERVICE" "$PREVIOUS_RENDERD_IMAGE_ID" || service_rc=1
			if (( service_rc == 0 && PREVIOUS_RENDERD_WAS_RUNNING == 0 )); then
				stop_rollback_service "$RENDERD_SERVICE" || service_rc=1
			fi
			(( service_rc == 0 )) || rollback_rc=1
		fi
		if (( rollback_image_ready == 1 )) && [[ -n "$PREVIOUS_CONTAINER_ID" ]]; then
			service_rc=0
			start_rollback_service "$SERVICE" "$PREVIOUS_IMAGE_ID" || service_rc=1
			if (( service_rc == 0 && PREVIOUS_WAS_RUNNING == 0 )); then
				stop_rollback_service "$SERVICE" || service_rc=1
			fi
			(( service_rc == 0 )) || rollback_rc=1
		fi
	fi
	return "$rollback_rc"
}

on_signal() {
	local signal="$1"
	case "$signal" in
		INT) on_error 130 ;;
		TERM) on_error 143 ;;
		HUP) on_error 129 ;;
		*) on_error 1 ;;
	esac
}

on_exit() {
	local rc="$1"
	if (( DEPLOY_SUCCEEDED == 0 && HANDLING_ERROR == 0 )); then
		(( rc != 0 )) || rc=1
		on_error "$rc"
	fi
	return "$rc"
}

on_error() {
	local rc="$1"
	if (( HANDLING_ERROR == 1 )); then
		exit "$rc"
	fi
	HANDLING_ERROR=1
	# Freeze all asynchronous/error handlers while rollback is running. This
	# keeps a second signal or a rollback probe from recursively aborting it.
	trap - ERR EXIT
	trap '' INT TERM HUP
	set +e
	collect_diagnostics
	if (( SERVICE_STOP_ATTEMPTED == 1 || PROMOTION_STARTED == 1 )); then
		if rollback_transaction; then
			ROLLBACK_OK=1
		fi
	fi
	log "deployment failed for revision $REVISION; diagnostics retained at $DIAGNOSTIC_DIR"
	if (( SERVICE_STOP_ATTEMPTED == 0 && PROMOTION_STARTED == 0 )); then
		log "running service and database were not changed"
	elif (( ROLLBACK_OK == 1 )); then
		log "previous service and database were restored"
	else
		log "automatic rollback was incomplete; inspect protected diagnostics before retrying"
	fi
	exit "$rc"
}
trap 'on_error "$?"' ERR
trap 'on_signal INT' INT
trap 'on_signal TERM' TERM
trap 'on_signal HUP' HUP
trap 'on_exit "$?"' EXIT

log "staging revision $REVISION"

# Validate the staged Compose model before inspecting the running service.
compose_stage config --quiet >"$STAGE/config.log" 2>&1
chmod 600 "$STAGE/config.log"
if ! STAGED_SERVICES="$(compose_stage_base config --services 2>>"$STAGE/config.log")"; then
	fail "could not enumerate staged Compose services"
fi
printf '%s\n' "$STAGED_SERVICES" | grep -Fxq bot || fail "staged Compose file must declare bot"
printf '%s\n' "$STAGED_SERVICES" | grep -Fxq "$RENDERD_SERVICE" || fail "staged Compose file must declare renderd"

# Without the active Compose file there is no trustworthy configuration from
# which to capture rollback images or running state. Refuse to adopt containers
# that happen to share the staged project's labels.
if [[ ! -e "$ACTIVE_COMPOSE" && ! -L "$ACTIVE_COMPOSE" ]]; then
	if ! UNTRACKED_CONTAINER_IDS="$(compose_stage ps -aq "$SERVICE" "$RENDERD_SERVICE" 2>/dev/null)"; then
		fail "could not inspect the staged Compose project for existing containers"
	fi
	[[ -z "$UNTRACKED_CONTAINER_IDS" ]] || fail "active Compose file is missing but project containers already exist"
fi

# Preserve the exact image references before building. A same-revision build
# replaces revision tags with new OCI manifests; after that Docker can no
# longer tag the manifests used by the running containers even though those
# containers remain healthy.
PREVIOUS_SERVICES=""
if [[ -e "$ACTIVE_COMPOSE" || -L "$ACTIVE_COMPOSE" ]]; then
	[[ -f "$ACTIVE_COMPOSE" && ! -L "$ACTIVE_COMPOSE" ]] || fail "active compose file is not a regular file"
	if ! PREVIOUS_SERVICES="$(compose_existing config --services 2>/dev/null)"; then
		fail "could not inspect the active Compose services"
	fi
	printf '%s\n' "$PREVIOUS_SERVICES" | grep -Fxq "$SERVICE" || fail "active Compose file must declare bot"
	if printf '%s\n' "$PREVIOUS_SERVICES" | grep -Fxq "$RENDERD_SERVICE"; then
		PREVIOUS_HAS_RENDERD=1
	fi
fi

PREVIOUS_CONTAINER_IDS=""
if [[ -n "$PREVIOUS_SERVICES" ]]; then
	if ! PREVIOUS_CONTAINER_IDS="$(compose_existing ps -aq "$SERVICE" 2>/dev/null)"; then
		fail "could not inspect the previous bot container"
	fi
fi
if [[ -n "$PREVIOUS_CONTAINER_IDS" ]]; then
	[[ "$PREVIOUS_CONTAINER_IDS" != *$'\n'* ]] || fail "more than one previous service container was found"
	PREVIOUS_CONTAINER_ID="$PREVIOUS_CONTAINER_IDS"
	is_container_id "$PREVIOUS_CONTAINER_ID" || fail "previous container id is invalid"
	PREVIOUS_RUNNING_STATE="$(docker inspect --format '{{.State.Running}}' "$PREVIOUS_CONTAINER_ID")"
	if [[ "$PREVIOUS_RUNNING_STATE" == true ]]; then
		PREVIOUS_WAS_RUNNING=1
	fi
	PREVIOUS_IMAGE_ID="$(docker inspect --format '{{.Image}}' "$PREVIOUS_CONTAINER_ID")"
	[[ "$PREVIOUS_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "previous image id is invalid"
	PREVIOUS_IMAGE_REFERENCE="$(docker inspect --format '{{.Config.Image}}' "$PREVIOUS_CONTAINER_ID")"
	[[ -n "$PREVIOUS_IMAGE_REFERENCE" && "$PREVIOUS_IMAGE_REFERENCE" != -* && "$PREVIOUS_IMAGE_REFERENCE" != *[[:space:]]* ]] || fail "previous image reference is invalid"
	preserve_image_tag "$PREVIOUS_IMAGE_ID" "$PREVIOUS_IMAGE" || fail "could not preserve previous bot image"
fi

if (( PREVIOUS_HAS_RENDERD == 1 )); then
	if ! PREVIOUS_RENDERD_CONTAINER_IDS="$(compose_existing ps -aq "$RENDERD_SERVICE" 2>/dev/null)"; then
		fail "could not inspect the previous renderd container"
	fi
	if [[ -n "$PREVIOUS_RENDERD_CONTAINER_IDS" ]]; then
		[[ "$PREVIOUS_RENDERD_CONTAINER_IDS" != *$'\n'* ]] || fail "more than one previous renderd container was found"
		PREVIOUS_RENDERD_CONTAINER_ID="$PREVIOUS_RENDERD_CONTAINER_IDS"
		is_container_id "$PREVIOUS_RENDERD_CONTAINER_ID" || fail "previous renderd container id is invalid"
		PREVIOUS_RENDERD_RUNNING_STATE="$(docker inspect --format '{{.State.Running}}' "$PREVIOUS_RENDERD_CONTAINER_ID")"
		if [[ "$PREVIOUS_RENDERD_RUNNING_STATE" == true ]]; then
			PREVIOUS_RENDERD_WAS_RUNNING=1
		fi
		PREVIOUS_RENDERD_IMAGE_ID="$(docker inspect --format '{{.Image}}' "$PREVIOUS_RENDERD_CONTAINER_ID")"
		[[ "$PREVIOUS_RENDERD_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "previous renderd image id is invalid"
		PREVIOUS_RENDERD_IMAGE_REFERENCE="$(docker inspect --format '{{.Config.Image}}' "$PREVIOUS_RENDERD_CONTAINER_ID")"
		[[ -n "$PREVIOUS_RENDERD_IMAGE_REFERENCE" && "$PREVIOUS_RENDERD_IMAGE_REFERENCE" != -* && "$PREVIOUS_RENDERD_IMAGE_REFERENCE" != *[[:space:]]* ]] || fail "previous renderd image reference is invalid"
		preserve_image_tag "$PREVIOUS_RENDERD_IMAGE_ID" "$PREVIOUS_RENDERD_IMAGE" || fail "could not preserve previous renderd image"
	fi
fi

# Build both immutable images only after all rollback tags are safe. The
# running services are still untouched throughout this step.
compose_stage build >"$STAGE/build.log" 2>&1
NEW_IMAGE_ID="$(docker image inspect --format '{{.Id}}' "$NEW_IMAGE")"
[[ "$NEW_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "built image id is invalid"
NEW_RENDERD_IMAGE_ID="$(docker image inspect --format '{{.Id}}' "$NEW_RENDERD_IMAGE")"
[[ "$NEW_RENDERD_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "built renderd image id is invalid"
chmod 600 "$STAGE/build.log"

if [[ -e "$ROOT/src" || -L "$ROOT/src" ]]; then
	[[ -d "$ROOT/src" && ! -L "$ROOT/src" ]] || fail "active source path is not a directory"
fi
for path in "$ROOT/compose.yaml" "$ROOT/.env" "$REVISION_FILE" "$REVISION_TEMP_FILE" "$STATE_FILE" "$STATE_TEMP_FILE"; do
	[[ ! -L "$path" ]] || fail "active deployment file is a symlink: $path"
done

cat >"$ROLLBACK/previous-state" <<EOF
revision=$PREVIOUS_REVISION
container_id=${PREVIOUS_CONTAINER_ID:-none}
running=$PREVIOUS_WAS_RUNNING
image_id=${PREVIOUS_IMAGE_ID:-none}
image_reference=${PREVIOUS_IMAGE_REFERENCE:-none}
rollback_image=${PREVIOUS_IMAGE:-none}
renderd_container_id=${PREVIOUS_RENDERD_CONTAINER_ID:-none}
renderd_running=$PREVIOUS_RENDERD_WAS_RUNNING
renderd_image_id=${PREVIOUS_RENDERD_IMAGE_ID:-none}
renderd_image_reference=${PREVIOUS_RENDERD_IMAGE_REFERENCE:-none}
renderd_rollback_image=${PREVIOUS_RENDERD_IMAGE:-none}
source=src
compose=compose.yaml
env=.env
database=$DB_PATH
state=.deploy-state
state_present=$PREVIOUS_STATE_PRESENT
EOF
chmod 600 "$ROLLBACK/previous-state"
printf '%s\n' "$PREVIOUS_REVISION" >"$ROLLBACK/previous-revision"
chmod 600 "$ROLLBACK/previous-revision"
if (( PREVIOUS_STATE_PRESENT == 1 )); then
	cp -- "$STATE_FILE" "$ROLLBACK/previous-state.snapshot"
	chmod 600 "$ROLLBACK/previous-state.snapshot"
fi
if (( PREVIOUS_REVISION_PRESENT == 1 )); then
	cp -- "$REVISION_FILE" "$ROLLBACK/.deploy-revision"
	chmod 600 "$ROLLBACK/.deploy-revision"
fi

log "quiescing service and checkpointing SQLite"
if [[ -n "$PREVIOUS_SERVICES" ]]; then
	SERVICE_STOP_ATTEMPTED=1
	if (( PREVIOUS_HAS_RENDERD == 1 )); then
		compose_existing stop "$SERVICE" "$RENDERD_SERVICE" >"$STAGE/stop.log" 2>&1
	else
		compose_existing stop "$SERVICE" >"$STAGE/stop.log" 2>&1
	fi
	if [[ -n "$PREVIOUS_CONTAINER_ID" ]]; then
		wait_for_stopped "$PREVIOUS_CONTAINER_ID" "previous service" || fail "previous service did not stop"
	fi
	if [[ -n "$PREVIOUS_RENDERD_CONTAINER_ID" ]]; then
		wait_for_stopped "$PREVIOUS_RENDERD_CONTAINER_ID" "previous renderd service" || fail "previous renderd service did not stop"
	fi
fi
SERVICE_QUIESCED=1

if [[ -e "$DB_PATH" || -L "$DB_PATH" ]]; then
	[[ -f "$DB_PATH" && ! -L "$DB_PATH" ]] || fail "database path is not a regular file"
	PREVIOUS_DB_PRESENT=1
elif [[ -e "$DB_PATH-wal" || -e "$DB_PATH-shm" ]]; then
	fail "SQLite sidecar exists without its database"
fi
DB_STATE_KNOWN=1

if (( PREVIOUS_DB_PRESENT == 1 )); then
	python3 - "$DB_PATH" "$ROLLBACK/database.sqlite" <<'PY'
import os
import sqlite3
import sys

source_path, backup_path = sys.argv[1:]
temporary_path = backup_path + ".tmp"
for path in (backup_path, temporary_path):
    try:
        os.unlink(path)
    except FileNotFoundError:
        pass

source = sqlite3.connect(source_path, timeout=30)
destination = None
try:
    source.execute("PRAGMA busy_timeout=30000")
    checkpoint = source.execute("PRAGMA wal_checkpoint(TRUNCATE)").fetchone()
    if checkpoint and checkpoint[0] != 0:
        raise RuntimeError(f"SQLite WAL checkpoint is busy: {checkpoint[0]}")
    check = source.execute("PRAGMA integrity_check").fetchone()
    if not check or check[0] != "ok":
        raise RuntimeError(f"SQLite integrity check failed: {check[0] if check else 'empty result'}")
    destination = sqlite3.connect(temporary_path)
    source.backup(destination)
    destination.execute("PRAGMA journal_mode=DELETE")
    check = destination.execute("PRAGMA integrity_check").fetchone()
    if not check or check[0] != "ok":
        raise RuntimeError(f"SQLite backup integrity check failed: {check[0] if check else 'empty result'}")
    destination.commit()
finally:
    if destination is not None:
        destination.close()
    source.close()

wal_path = source_path + "-wal"
if os.path.exists(wal_path) and os.path.getsize(wal_path) != 0:
    raise RuntimeError("SQLite WAL remained after checkpoint")
os.replace(temporary_path, backup_path)
os.chmod(backup_path, 0o600)
PY
	BACKUP_CREATED=1
	chmod 600 "$ROLLBACK/database.sqlite"
fi

run_migration_gate() {
	local migration_db_path="$MIGRATION_DATA_DIR/$DB_RELATIVE_PATH"
	local migration_db_parent
	migration_db_parent="$(dirname "$migration_db_path")"
	[[ ! -e "$MIGRATION_DATA_DIR" && ! -L "$MIGRATION_DATA_DIR" ]] || fail "migration data directory already exists"
	assert_no_symlink_components "$migration_db_parent"
	mkdir -p "$migration_db_parent"
	assert_no_symlink_components "$migration_db_parent"
	[[ ! -L "$MIGRATION_DATA_DIR" && ! -L "$migration_db_parent" && ! -L "$migration_db_path" ]] || fail "migration data path contains a symlink"
	chmod 700 "$MIGRATION_DATA_DIR" "$migration_db_parent"
	if (( PREVIOUS_DB_PRESENT == 1 )); then
		cp -- "$ROLLBACK/database.sqlite" "$migration_db_path"
	else
		: >"$migration_db_path"
	fi
	chmod 600 "$migration_db_path"
	chown 10001:10001 "$MIGRATION_DATA_DIR" "$migration_db_parent" "$migration_db_path"
	verify_image_tag "$NEW_IMAGE" "$NEW_IMAGE_ID" || fail "new bot image tag changed before migration"
	: >"$MIGRATION_LOG"
	chmod 600 "$MIGRATION_LOG"
	log "running isolated migration gate against the immutable image"
	docker run --rm \
		--name "deploy-$DEPLOY_ID-migrate" \
		--network none \
		--user 10001:10001 \
		--mount "type=bind,src=$MIGRATION_DATA_DIR,dst=/data" \
		--env "BOT_DB_PATH=$DB_CONTAINER_PATH" \
		--env "BOT_BUILD_VERSION=$REVISION" \
		--env "LIFE_USTC_SERVER=http://127.0.0.1:9" \
		--env "BOT_HEALTH_ADDR=127.0.0.1:2282" \
		--env BOT_ENABLE_NAPCAT_BRIDGE=false \
		--env BOT_ENABLE_QQ_BOT=false \
		--env BOT_ENABLE_QQ_BOT_GATEWAY=false \
		--env BOT_ENABLE_QQ_BOT_WEBHOOK=false \
		--env BOT_ENABLE_AGENT=false \
		--env BOT_ENABLE_IMAGE_RESPONSES=false \
		"$NEW_IMAGE_ID" migrate >"$MIGRATION_LOG" 2>&1
	python3 - "$migration_db_path" <<'PY' >>"$MIGRATION_LOG" 2>&1
import os
import sqlite3
import sys

database_path = sys.argv[1]
if not os.path.isfile(database_path) or os.path.islink(database_path):
    raise RuntimeError("migration database is not a regular file")
connection = sqlite3.connect(database_path, timeout=30)
try:
    version = connection.execute("PRAGMA user_version").fetchone()[0]
    if version != 2:
        raise RuntimeError(f"unexpected SQLite user_version: {version}")
    integrity = connection.execute("PRAGMA integrity_check").fetchone()
    if not integrity or integrity[0] != "ok":
        raise RuntimeError(f"SQLite integrity check failed: {integrity[0] if integrity else 'empty result'}")
finally:
    connection.close()
PY
	MIGRATION_GATE_COMPLETE=1
	log "isolated migration gate passed"
}

run_migration_gate

# Move old files into the per-deployment rollback directory and promote the
# already-built stage. Each move is tracked so a partial promotion is
# recoverable without deleting a broad directory.
PROMOTION_STARTED=1
if [[ -e "$ROOT/src" || -L "$ROOT/src" ]]; then
	OLD_SRC_MOVED=1
	mv -- "$ROOT/src" "$ROLLBACK/src"
fi
NEW_SRC_MOVED=1
	mv -- "$STAGE/src" "$ROOT/src"
if [[ -e "$ROOT/compose.yaml" || -L "$ROOT/compose.yaml" ]]; then
	OLD_COMPOSE_MOVED=1
	mv -- "$ROOT/compose.yaml" "$ROLLBACK/compose.yaml"
fi
NEW_COMPOSE_MOVED=1
	mv -- "$STAGE/compose.yaml" "$ROOT/compose.yaml"
if [[ -e "$ROOT/.env" || -L "$ROOT/.env" ]]; then
	OLD_ENV_MOVED=1
	mv -- "$ROOT/.env" "$ROLLBACK/.env"
fi
NEW_ENV_MOVED=1
mv -- "$STAGE/.env" "$ROOT/.env"
chmod 600 "$ROOT/.env"
REVISION_WRITE_STARTED=1
printf '%s\n' "$REVISION" >"$REVISION_TEMP_FILE"
chmod 600 "$REVISION_TEMP_FILE"
NEW_REVISION_WRITTEN=1
mv -- "$REVISION_TEMP_FILE" "$REVISION_FILE"

log "starting revision $REVISION and waiting for health"
# Start the sidecar explicitly and without Compose dependency waits. This
# keeps the script's health timeout as the upper bound for a broken renderer.
verify_image_tag "$NEW_IMAGE" "$NEW_IMAGE_ID" || fail "new bot image tag changed before startup"
verify_image_tag "$NEW_RENDERD_IMAGE" "$NEW_RENDERD_IMAGE_ID" || fail "new renderd image tag changed before startup"
STARTUP_ATTEMPTED=1
compose_active_new up -d --pull never --no-build --no-deps "$RENDERD_SERVICE" >"$STAGE/start.log" 2>&1
wait_for_healthy active "$RENDERD_SERVICE" || fail "new renderd service did not become healthy within ${HEALTH_TIMEOUT}s"
[[ "$NEW_RENDERD_CONTAINER_ID" =~ ^[0-9a-f]{12,64}$ ]] || fail "new renderd container id is invalid"
compose_active_new up -d --pull never --no-build --no-deps "$SERVICE" >>"$STAGE/start.log" 2>&1
wait_for_healthy active "$SERVICE" || fail "new service did not become healthy within ${HEALTH_TIMEOUT}s"

RUNNING_IMAGE_ID="$(docker inspect --format '{{.Image}}' "$NEW_CONTAINER_ID")"
[[ "$RUNNING_IMAGE_ID" == "$NEW_IMAGE_ID" ]] || fail "running image does not match the staged image"
RUNNING_RENDERD_IMAGE_ID="$(docker inspect --format '{{.Image}}' "$NEW_RENDERD_CONTAINER_ID")"
[[ "$RUNNING_RENDERD_IMAGE_ID" == "$NEW_RENDERD_IMAGE_ID" ]] || fail "running renderd image does not match the staged image"
DEPLOYED_BUILD_VERSION="$(docker inspect --format '{{range .Config.Env}}{{if eq (index (split . "=") 0) "BOT_BUILD_VERSION"}}{{index (split . "=") 1}}{{end}}{{end}}' "$NEW_CONTAINER_ID")"
[[ "$DEPLOYED_BUILD_VERSION" == "$REVISION" ]] || fail "deployed BOT_BUILD_VERSION does not match revision"
[[ "$(tr -d '\r\n' <"$REVISION_FILE")" == "$REVISION" ]] || fail "deployed revision marker does not match"

STATE_WRITE_STARTED=1
cat >"$STATE_TEMP_FILE" <<EOF
revision=$REVISION
image=$NEW_IMAGE
image_id=$NEW_IMAGE_ID
container_id=$NEW_CONTAINER_ID
renderd_image=$NEW_RENDERD_IMAGE
renderd_image_id=$NEW_RENDERD_IMAGE_ID
renderd_container_id=$NEW_RENDERD_CONTAINER_ID
deployed_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
source=src
compose=compose.yaml
database=$DB_PATH
previous_rollback=$ROLLBACK
EOF
chmod 600 "$STATE_TEMP_FILE"
mv -- "$STATE_TEMP_FILE" "$STATE_FILE"

log "deployment succeeded: revision $REVISION, images $NEW_IMAGE and $NEW_RENDERD_IMAGE"
trap '' INT TERM HUP
trap - ERR EXIT
DEPLOY_SUCCEEDED=1
exit 0
REMOTE_DEPLOY
