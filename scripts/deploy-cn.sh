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
[[ "$SERVICE" =~ ^[a-z][a-z0-9_-]*$ ]] || die "SERVICE contains unsupported characters"
[[ "$ENV_FILE" =~ ^[A-Za-z0-9._-]+$ ]] || die "ENV_FILE must be a file name in the checkout"
[[ "$DRY_RUN" == 0 || "$DRY_RUN" == 1 ]] || die "DEPLOY_DRY_RUN must be 0 or 1"
[[ "$HEALTH_TIMEOUT" =~ ^[0-9]+$ ]] || die "DEPLOY_HEALTH_TIMEOUT must be a number of seconds"
(( HEALTH_TIMEOUT >= 1 && HEALTH_TIMEOUT <= 3600 )) || die "DEPLOY_HEALTH_TIMEOUT must be between 1 and 3600 seconds"
[[ "$IMAGE_REPOSITORY" =~ ^[a-z0-9]+([._/-][a-z0-9]+)*$ ]] || die "DEPLOY_IMAGE_REPOSITORY contains unsupported characters"

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
REMOTE_DEPLOY_ID_ARG="$(quote_remote_arg "$DEPLOY_ID")"

# All remote command output is retained under a mode-700 stage directory. FD
# 3 is the only channel used for concise, non-secret operator status.
ssh "$REMOTE_HOST" "bash -s -- $REMOTE_ROOT_ARG $REMOTE_STAGE_ARG $REMOTE_ROLLBACK_ARG $REMOTE_REVISION_ARG $REMOTE_SERVICE_ARG $REMOTE_TIMEOUT_ARG $REMOTE_IMAGE_REPOSITORY_ARG $REMOTE_DEPLOY_ID_ARG" <<'REMOTE_DEPLOY'
#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

ROOT="$1"
STAGE="$2"
ROLLBACK="$3"
REVISION="$4"
SERVICE="$5"
HEALTH_TIMEOUT="$6"
IMAGE_REPOSITORY="$7"
DEPLOY_ID="$8"

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

[[ "$ROOT" == /* && "$ROOT" != "/" ]] || fail "invalid remote root"
[[ "$STAGE" == "$ROOT/.deploy-staging/"* ]] || fail "invalid remote stage"
[[ "$ROLLBACK" == "$ROOT/.deploy-rollback/"* ]] || fail "invalid remote rollback directory"
[[ "$REVISION" =~ ^[0-9a-f]{40}$ ]] || fail "invalid revision"
[[ "$SERVICE" =~ ^[a-z][a-z0-9_-]*$ ]] || fail "invalid service"
[[ "$HEALTH_TIMEOUT" =~ ^[0-9]+$ ]] || fail "invalid health timeout"
(( HEALTH_TIMEOUT >= 1 && HEALTH_TIMEOUT <= 3600 )) || fail "health timeout out of range"
[[ "$IMAGE_REPOSITORY" =~ ^[a-z0-9]+([._/-][a-z0-9]+)*$ ]] || fail "invalid image repository"
[[ "$DEPLOY_ID" =~ ^[0-9A-Za-z_.-]+$ ]] || fail "invalid deployment id"

DATA_DIR="$ROOT/data"
ACTIVE_COMPOSE="$ROOT/compose.yaml"
ACTIVE_ENV="$ROOT/.env"
NEW_IMAGE="$IMAGE_REPOSITORY:$REVISION"
PREVIOUS_IMAGE="$IMAGE_REPOSITORY:rollback-$DEPLOY_ID"
STAGE_IMAGE_FILE="$STAGE/image.yaml"
ROLLBACK_IMAGE_FILE="$ROLLBACK/image.yaml"
DIAGNOSTIC_DIR="$STAGE/diagnostics"
FAILED_ACTIVE_DIR="$STAGE/failed-active"
MIGRATION_DATA_DIR="$STAGE/migration-data"
MIGRATION_LOG="$STAGE/migration.log"
LOCK_FILE="$ROOT/.deploy.lock"

PREVIOUS_CONTAINER_ID=""
PREVIOUS_IMAGE_ID=""
PREVIOUS_IMAGE_REFERENCE=""
PREVIOUS_REVISION="unknown"
PREVIOUS_WAS_RUNNING=0
PREVIOUS_DB_PRESENT=0
DB_STATE_KNOWN=0
BACKUP_CREATED=0
SERVICE_QUIESCED=0
PROMOTION_STARTED=0
OLD_SRC_MOVED=0
NEW_SRC_MOVED=0
OLD_COMPOSE_MOVED=0
NEW_COMPOSE_MOVED=0
OLD_ENV_MOVED=0
NEW_ENV_MOVED=0
NEW_REVISION_WRITTEN=0
NEW_CONTAINER_ID=""
NEW_IMAGE_ID=""
MIGRATION_GATE_COMPLETE=0
ROLLBACK_OK=0
HANDLING_ERROR=0

preflight_error() {
	local rc="$1"
	log "deployment failed during remote preflight; diagnostics retained at $DIAGNOSTIC_DIR"
	exit "$rc"
}
trap 'preflight_error "$?"' ERR

if [[ -f "$ROOT/.deploy-revision" && ! -L "$ROOT/.deploy-revision" ]]; then
	PREVIOUS_REVISION="$(tr -d '\r\n' <"$ROOT/.deploy-revision")"
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

chmod 600 "$STAGE/.env"
if [[ "$(grep -c '^BOT_BUILD_VERSION=' "$STAGE/.env" || true)" -ne 0 ]]; then
	# A staged copy may have inherited an old value from the checkout. Remove
	# every old entry before writing the exact archived revision.
	sed -i '/^BOT_BUILD_VERSION=/d' "$STAGE/.env"
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

# Compose's image field is supplied only in this revision-scoped overlay. A
# tag per revision preserves the old image even if Compose later recreates the
# service, and makes the image used at runtime directly verifiable.
cat >"$STAGE_IMAGE_FILE" <<EOF
services:
  $SERVICE:
    image: $NEW_IMAGE
EOF
chmod 600 "$STAGE_IMAGE_FILE"

compose_stage() {
	docker compose --project-directory "$STAGE" --env-file "$STAGE/.env" \
		-f "$STAGE/compose.yaml" -f "$STAGE_IMAGE_FILE" "$@"
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
	docker compose --project-directory "$ROOT" --env-file "$ACTIVE_ENV" \
		-f "$ACTIVE_COMPOSE" -f "$ROLLBACK_IMAGE_FILE" "$@"
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
	} >"$target" 2>&1 || true
	chmod 600 "$target" 2>/dev/null || true
	if [[ -f "$ACTIVE_COMPOSE" && -f "$ACTIVE_ENV" ]]; then
		compose_active_new logs --no-color --tail=200 "$SERVICE" >"$DIAGNOSTIC_DIR/service.log" 2>&1 || true
		chmod 600 "$DIAGNOSTIC_DIR/service.log" 2>/dev/null || true
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
	local started
	local now
	local container_id
	local state
	local status
	started="$(date +%s)"
	while :; do
		if [[ "$mode" == active ]]; then
			container_id="$(compose_active_new ps -q "$SERVICE" 2>/dev/null || true)"
		else
			container_id="$(compose_rollback ps -q "$SERVICE" 2>/dev/null || true)"
		fi
		if [[ "$container_id" =~ ^[0-9a-f]{12,64}$ ]]; then
			state="$(docker inspect --format '{{.State.Status}}' "$container_id" 2>/dev/null || true)"
			status="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}no-health{{end}}' "$container_id" 2>/dev/null || true)"
			case "$status" in
				healthy)
					if [[ "$state" == running ]]; then
						if [[ "$mode" == active ]]; then
							NEW_CONTAINER_ID="$container_id"
						else
							PREVIOUS_CONTAINER_ID="$container_id"
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

restore_database() {
	local failed_database_dir="$STAGE/failed-database"
	if (( DB_STATE_KNOWN == 0 )); then
		return 1
	fi
	if (( PREVIOUS_DB_PRESENT == 1 && BACKUP_CREATED == 0 )); then
		# The backup was not proven complete. Leave the original database in
		# place instead of risking an irreversible move during rollback; no
		# replacement service has started while backup creation is in progress.
		return 0
	fi
	mkdir -p "$failed_database_dir"
	chmod 700 "$failed_database_dir"
	for path in "$DB_PATH" "$DB_PATH-wal" "$DB_PATH-shm"; do
		if [[ -e "$path" || -L "$path" ]]; then
			[[ ! -L "$path" ]] || return 1
			mv -- "$path" "$failed_database_dir/$(basename "$path")" || return 1
		fi
	done
	if (( PREVIOUS_DB_PRESENT == 1 )); then
		cp -- "$ROLLBACK/database.sqlite" "$DB_PATH" || return 1
		chmod 600 "$DB_PATH"
		chown 10001:10001 "$DB_PATH" || return 1
	fi
}

restore_promoted_files() {
	mkdir -p "$FAILED_ACTIVE_DIR"
	chmod 700 "$FAILED_ACTIVE_DIR"
	if (( NEW_REVISION_WRITTEN == 1 )); then
		mv -- "$ROOT/.deploy-revision" "$FAILED_ACTIVE_DIR/.deploy-revision" 2>/dev/null || true
	fi
	if (( NEW_ENV_MOVED == 1 )); then
		mv -- "$ROOT/.env" "$FAILED_ACTIVE_DIR/.env" 2>/dev/null || true
	fi
	if (( NEW_COMPOSE_MOVED == 1 )); then
		mv -- "$ROOT/compose.yaml" "$FAILED_ACTIVE_DIR/compose.yaml" 2>/dev/null || true
	fi
	if (( NEW_SRC_MOVED == 1 )); then
		mv -- "$ROOT/src" "$FAILED_ACTIVE_DIR/src" 2>/dev/null || true
	fi
	if (( OLD_SRC_MOVED == 1 )); then
		mv -- "$ROLLBACK/src" "$ROOT/src" || return 1
	fi
	if (( OLD_COMPOSE_MOVED == 1 )); then
		mv -- "$ROLLBACK/compose.yaml" "$ROOT/compose.yaml" || return 1
	fi
	if (( OLD_ENV_MOVED == 1 )); then
		mv -- "$ROLLBACK/.env" "$ROOT/.env" || return 1
	fi
	if [[ -f "$ROLLBACK/.deploy-revision" ]]; then
		mv -- "$ROLLBACK/.deploy-revision" "$ROOT/.deploy-revision" || return 1
	fi
}

rollback_transaction() {
	local rollback_rc=0
	set +e
	if [[ -n "$NEW_CONTAINER_ID" ]]; then
		docker stop "$NEW_CONTAINER_ID" >/dev/null 2>&1 || true
	fi
	if (( SERVICE_QUIESCED == 1 )); then
		restore_database || rollback_rc=1
	fi
	if (( PROMOTION_STARTED == 1 )); then
		restore_promoted_files || rollback_rc=1
	fi
	if (( rollback_rc == 0 && PREVIOUS_WAS_RUNNING == 1 )) && [[ -n "$PREVIOUS_IMAGE_ID" ]]; then
		if [[ ! -f "$ROLLBACK_IMAGE_FILE" ]]; then
			printf 'services:\n  %s:\n    image: %s\n' "$SERVICE" "$PREVIOUS_IMAGE" >"$ROLLBACK_IMAGE_FILE"
			chmod 600 "$ROLLBACK_IMAGE_FILE"
		fi
		compose_rollback up -d --no-build "$SERVICE" >"$DIAGNOSTIC_DIR/rollback-start.log" 2>&1 || rollback_rc=1
		if (( rollback_rc == 0 )); then
			wait_for_healthy rollback || rollback_rc=1
		fi
	fi
	set -e
	return "$rollback_rc"
}

on_error() {
	local rc="$1"
	if (( HANDLING_ERROR == 1 )); then
		exit "$rc"
	fi
	HANDLING_ERROR=1
	trap - ERR
	collect_diagnostics
	if (( SERVICE_QUIESCED == 1 || PROMOTION_STARTED == 1 )); then
		if rollback_transaction; then
			ROLLBACK_OK=1
		fi
	fi
	log "deployment failed for revision $REVISION; diagnostics retained at $DIAGNOSTIC_DIR"
	if (( SERVICE_QUIESCED == 0 && PROMOTION_STARTED == 0 )); then
		log "running service and database were not changed"
	elif (( ROLLBACK_OK == 1 )); then
		log "previous service and database were restored"
	else
		log "automatic rollback was incomplete; inspect protected diagnostics before retrying"
	fi
	exit "$rc"
}
trap 'on_error "$?"' ERR

log "staging revision $REVISION"

# Validate the staged Compose model before inspecting the running service.
compose_stage config --quiet >"$STAGE/config.log" 2>&1
chmod 600 "$STAGE/config.log"

# Preserve the exact image reference before building. A same-revision build
# replaces the revision tag with a new OCI manifest when provenance changes;
# after that replacement Docker can no longer tag the manifest used by the
# running container even though the container itself remains healthy.
PREVIOUS_CONTAINER_IDS="$(compose_existing ps -q "$SERVICE" 2>/dev/null || true)"
if [[ -n "$PREVIOUS_CONTAINER_IDS" ]]; then
	[[ "$PREVIOUS_CONTAINER_IDS" != *$'\n'* ]] || fail "more than one previous service container was found"
	PREVIOUS_CONTAINER_ID="$PREVIOUS_CONTAINER_IDS"
	[[ "$PREVIOUS_CONTAINER_ID" =~ ^[0-9a-f]{12,64}$ ]] || fail "previous container id is invalid"
	PREVIOUS_RUNNING_STATE="$(docker inspect --format '{{.State.Running}}' "$PREVIOUS_CONTAINER_ID")"
	if [[ "$PREVIOUS_RUNNING_STATE" == true ]]; then
		PREVIOUS_WAS_RUNNING=1
	fi
	PREVIOUS_IMAGE_ID="$(docker inspect --format '{{.Image}}' "$PREVIOUS_CONTAINER_ID")"
	[[ "$PREVIOUS_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "previous image id is invalid"
	PREVIOUS_IMAGE_REFERENCE="$(docker inspect --format '{{.Config.Image}}' "$PREVIOUS_CONTAINER_ID")"
	[[ -n "$PREVIOUS_IMAGE_REFERENCE" && "$PREVIOUS_IMAGE_REFERENCE" != -* && "$PREVIOUS_IMAGE_REFERENCE" != *[[:space:]]* ]] || fail "previous image reference is invalid"
	docker image inspect "$PREVIOUS_IMAGE_REFERENCE" >/dev/null
	docker tag "$PREVIOUS_IMAGE_REFERENCE" "$PREVIOUS_IMAGE"
fi

# Build the immutable image only after the rollback tag is safe. The running
# service is still untouched throughout this step.
compose_stage build "$SERVICE" >"$STAGE/build.log" 2>&1
NEW_IMAGE_ID="$(docker image inspect --format '{{.Id}}' "$NEW_IMAGE")"
[[ "$NEW_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "built image id is invalid"
chmod 600 "$STAGE/build.log"

if [[ -e "$ROOT/src" || -L "$ROOT/src" ]]; then
	[[ -d "$ROOT/src" && ! -L "$ROOT/src" ]] || fail "active source path is not a directory"
fi
for path in "$ROOT/compose.yaml" "$ROOT/.env" "$ROOT/.deploy-revision"; do
	[[ ! -L "$path" ]] || fail "active deployment file is a symlink: $path"
done

cat >"$ROLLBACK/previous-state" <<EOF
revision=$PREVIOUS_REVISION
container_id=${PREVIOUS_CONTAINER_ID:-none}
running=$PREVIOUS_WAS_RUNNING
image_id=${PREVIOUS_IMAGE_ID:-none}
image_reference=${PREVIOUS_IMAGE_REFERENCE:-none}
rollback_image=${PREVIOUS_IMAGE:-none}
source=src
compose=compose.yaml
env=.env
database=$DB_PATH
EOF
chmod 600 "$ROLLBACK/previous-state"
printf '%s\n' "$PREVIOUS_REVISION" >"$ROLLBACK/previous-revision"
chmod 600 "$ROLLBACK/previous-revision"
if [[ -f "$ROOT/.deploy-state" && ! -L "$ROOT/.deploy-state" ]]; then
	cp -- "$ROOT/.deploy-state" "$ROLLBACK/previous-state.snapshot"
	chmod 600 "$ROLLBACK/previous-state.snapshot"
fi

log "quiescing service and checkpointing SQLite"
compose_existing stop "$SERVICE" >"$STAGE/stop.log" 2>&1
SERVICE_QUIESCED=1
if [[ -n "$PREVIOUS_CONTAINER_ID" ]]; then
	for _ in 1 2 3 4 5; do
		[[ "$(docker inspect --format '{{.State.Running}}' "$PREVIOUS_CONTAINER_ID" 2>/dev/null || true)" != true ]] && break
		sleep 1
	done
	[[ "$(docker inspect --format '{{.State.Running}}' "$PREVIOUS_CONTAINER_ID" 2>/dev/null || true)" != true ]] || fail "previous service did not stop"
fi

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
		"$NEW_IMAGE" migrate >"$MIGRATION_LOG" 2>&1
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
if [[ -e "$ROOT/src" ]]; then
	mv -- "$ROOT/src" "$ROLLBACK/src"
	OLD_SRC_MOVED=1
fi
mv -- "$STAGE/src" "$ROOT/src"
NEW_SRC_MOVED=1
if [[ -e "$ROOT/compose.yaml" ]]; then
	mv -- "$ROOT/compose.yaml" "$ROLLBACK/compose.yaml"
	OLD_COMPOSE_MOVED=1
fi
mv -- "$STAGE/compose.yaml" "$ROOT/compose.yaml"
NEW_COMPOSE_MOVED=1
if [[ -e "$ROOT/.env" ]]; then
	mv -- "$ROOT/.env" "$ROLLBACK/.env"
	OLD_ENV_MOVED=1
fi
mv -- "$STAGE/.env" "$ROOT/.env"
NEW_ENV_MOVED=1
chmod 600 "$ROOT/.env"
if [[ -e "$ROOT/.deploy-revision" ]]; then
	cp -- "$ROOT/.deploy-revision" "$ROLLBACK/.deploy-revision"
	chmod 600 "$ROLLBACK/.deploy-revision"
fi
printf '%s\n' "$REVISION" >"$ROOT/.deploy-revision"
chmod 600 "$ROOT/.deploy-revision"
NEW_REVISION_WRITTEN=1

log "starting revision $REVISION and waiting for health"
compose_active_new up -d --no-build "$SERVICE" >"$STAGE/start.log" 2>&1
NEW_CONTAINER_ID="$(compose_active_new ps -q "$SERVICE")"
[[ "$NEW_CONTAINER_ID" =~ ^[0-9a-f]{12,64}$ ]] || fail "new container id is invalid"
wait_for_healthy active || fail "new service did not become healthy within ${HEALTH_TIMEOUT}s"

RUNNING_IMAGE_ID="$(docker inspect --format '{{.Image}}' "$NEW_CONTAINER_ID")"
[[ "$RUNNING_IMAGE_ID" == "$NEW_IMAGE_ID" ]] || fail "running image does not match the staged image"
DEPLOYED_BUILD_VERSION="$(docker inspect --format '{{range .Config.Env}}{{if eq (index (split . "=") 0) "BOT_BUILD_VERSION"}}{{index (split . "=") 1}}{{end}}{{end}}' "$NEW_CONTAINER_ID")"
[[ "$DEPLOYED_BUILD_VERSION" == "$REVISION" ]] || fail "deployed BOT_BUILD_VERSION does not match revision"
[[ "$(tr -d '\r\n' <"$ROOT/.deploy-revision")" == "$REVISION" ]] || fail "deployed revision marker does not match"

cat >"$ROOT/.deploy-state.tmp" <<EOF
revision=$REVISION
image=$NEW_IMAGE
image_id=$NEW_IMAGE_ID
container_id=$NEW_CONTAINER_ID
deployed_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
source=src
compose=compose.yaml
database=$DB_PATH
previous_rollback=$ROLLBACK
EOF
chmod 600 "$ROOT/.deploy-state.tmp"
mv -- "$ROOT/.deploy-state.tmp" "$ROOT/.deploy-state"

log "deployment succeeded: revision $REVISION, image $NEW_IMAGE"
exit 0
REMOTE_DEPLOY
