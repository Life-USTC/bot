#!/usr/bin/env bash
set -euo pipefail

# Build and deploy one committed revision natively on the macOS host.  The
# source archive is created locally, while both binaries are compiled on the
# target machine.  The remote transaction keeps its stage under build/ so a
# failed update can be inspected without changing the live tree.

REMOTE_HOST="${REMOTE_HOST:-tkm-mac-mini}"
REMOTE_USER="${REMOTE_USER:-tiankaima}"
REMOTE_ROOT="${REMOTE_ROOT:-/Users/tiankaima/Services/life-ustc-bot}"
DRY_RUN="${DEPLOY_DRY_RUN:-0}"
HEALTH_TIMEOUT="${DEPLOY_HEALTH_TIMEOUT:-180}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

die() {
	echo "deploy-mac: $*" >&2
	exit 1
}

require_command() {
	command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

validate_remote_path() {
	local path="$1"
	[[ "$path" == /* && "$path" != "/" && "$path" != */ ]] || die "REMOTE_ROOT must be a non-root absolute path without a trailing slash"
	[[ "$path" != *$'\n'* && "$path" != *$'\r'* ]] || die "REMOTE_ROOT contains a newline"
	local relative_path="${path#/}"
	[[ "$relative_path" != *//* ]] || die "REMOTE_ROOT contains an empty path component"
	local component
	local -a components
	IFS=/ read -r -a components <<<"$relative_path"
	for component in "${components[@]}"; do
		[[ -n "$component" && "$component" != "." && "$component" != ".." ]] || die "REMOTE_ROOT contains an empty or traversal component"
		[[ "$component" =~ ^[A-Za-z0-9._-]+$ ]] || die "REMOTE_ROOT contains an unsupported character"
	done
}

validate_remote_path "$REMOTE_ROOT"
[[ "$REMOTE_HOST" =~ ^[A-Za-z0-9._-]+$ ]] || die "REMOTE_HOST contains unsupported characters"
[[ "$REMOTE_USER" =~ ^[A-Za-z0-9._-]+$ ]] || die "REMOTE_USER contains unsupported characters"
[[ "$DRY_RUN" == 0 || "$DRY_RUN" == 1 ]] || die "DEPLOY_DRY_RUN must be 0 or 1"
[[ "$HEALTH_TIMEOUT" =~ ^[0-9]+$ ]] || die "DEPLOY_HEALTH_TIMEOUT must be a number of seconds"
(( HEALTH_TIMEOUT >= 1 && HEALTH_TIMEOUT <= 3600 )) || die "DEPLOY_HEALTH_TIMEOUT must be between 1 and 3600 seconds"

require_command git

if [[ -n "$(git -C "$ROOT" status --porcelain --untracked-files=all)" ]]; then
	if [[ "$DRY_RUN" == 0 ]]; then
		echo "refusing to deploy a dirty git checkout; commit or stash changes first" >&2
		git -C "$ROOT" status --short --untracked-files=all >&2
		exit 1
	fi
	echo "deploy-mac: dry-run from a dirty checkout; no remote changes will be made" >&2
fi

REVISION="$(git -C "$ROOT" rev-parse HEAD)"
[[ "$REVISION" =~ ^[0-9a-f]{40}$ ]] || die "git revision is not a 40-character lowercase SHA"

if [[ "$DRY_RUN" == 1 ]]; then
	echo "deploy-mac: dry-run would deploy revision $REVISION to $REMOTE_USER@$REMOTE_HOST:$REMOTE_ROOT"
	echo "deploy-mac: dry-run performs no SSH, source, binary, database, or launchd changes"
	exit 0
fi

require_command ssh
require_command tar
require_command go

quote_remote_arg() {
	local value="$1"
	printf "'%s'" "${value//\'/\'\\\'\'}"
}

DEPLOY_ID="$(date -u +%Y%m%dT%H%M%SZ)-$REVISION-$$"
REMOTE_STAGE="$REMOTE_ROOT/build/$DEPLOY_ID"
SSH_TARGET="$REMOTE_USER@$REMOTE_HOST"

REMOTE_ROOT_ARG="$(quote_remote_arg "$REMOTE_ROOT")"
REMOTE_STAGE_ARG="$(quote_remote_arg "$REMOTE_STAGE")"

LOCAL_WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/deploy-mac.XXXXXXXX")"
LOCAL_SOURCE_ROOT="$LOCAL_WORK_DIR/src"
LOCAL_SOURCE_ARCHIVE="$LOCAL_WORK_DIR/source.tar.gz"
trap 'rm -rf -- "$LOCAL_WORK_DIR"' EXIT

mkdir "$LOCAL_SOURCE_ROOT"

# Start with the exact tracked commit.  Dependencies are added only inside
# this temporary copy, so a deployment never modifies the checkout or sends
# ignored files such as config.json to the host.
git -C "$ROOT" archive --format=tar.gz --prefix=src/ "$REVISION" >"$LOCAL_SOURCE_ARCHIVE"
tar -xzf "$LOCAL_SOURCE_ARCHIVE" -C "$LOCAL_WORK_DIR"
[[ -f "$LOCAL_SOURCE_ROOT/go.mod" ]] || die "tracked archive is missing go.mod"
[[ -f "$LOCAL_SOURCE_ROOT/renderd/Cargo.lock" ]] || die "tracked archive is missing renderd/Cargo.lock"

echo "deploy-mac: vendoring Go dependencies in the temporary source archive"
(cd "$LOCAL_SOURCE_ROOT" && go mod vendor)
[[ -f "$LOCAL_SOURCE_ROOT/vendor/modules.txt" ]] || die "go mod vendor did not create vendor/modules.txt"

# Create only the revision-scoped stage.  No live binary, config, database, or
# launchd file is touched until the remote transaction has built both binaries.
ssh "$SSH_TARGET" "bash -s -- $REMOTE_ROOT_ARG $REMOTE_STAGE_ARG" <<'REMOTE_PREPARE'
set -euo pipefail
root="$1"
stage="$2"

[[ "$root" == /* && "$root" != / && "$root" != */ ]] || { echo "invalid remote root" >&2; exit 1; }
[[ "$stage" == "$root/build/"* ]] || { echo "invalid remote stage" >&2; exit 1; }
[[ ! -L "$root" ]] || { echo "remote root is a symlink" >&2; exit 1; }
mkdir -p "$root" "$root/bin" "$root/data" "$root/logs" "$root/build"
[[ ! -L "$root/bin" && ! -L "$root/data" && ! -L "$root/logs" && ! -L "$root/build" ]] || {
	echo "remote deployment directory contains a symlink" >&2
	exit 1
}
[[ ! -e "$stage" && ! -L "$stage" ]] || { echo "remote deployment stage already exists" >&2; exit 1; }
mkdir "$stage" "$stage/source" "$stage/bin" "$stage/rollback" "$stage/diagnostics"
chmod 700 "$root/build" "$stage" "$stage/source" "$stage/bin" "$stage/rollback" "$stage/diagnostics"
REMOTE_PREPARE

echo "deploy-mac: transferring the committed source archive to $SSH_TARGET"
tar -C "$LOCAL_SOURCE_ROOT" -czf - . |
	ssh "$SSH_TARGET" "tar -xzf - -C $REMOTE_STAGE_ARG/source"

REMOTE_REVISION_ARG="$(quote_remote_arg "$REVISION")"
REMOTE_DEPLOY_ID_ARG="$(quote_remote_arg "$DEPLOY_ID")"
REMOTE_TIMEOUT_ARG="$(quote_remote_arg "$HEALTH_TIMEOUT")"
REMOTE_USER_ARG="$(quote_remote_arg "$REMOTE_USER")"

# All remote command output is retained in a mode-600 stage log.  FD 3 is the
# only concise status channel sent back to the operator; config values are
# never shell-sourced or written to that channel.
ssh "$SSH_TARGET" "bash -s -- $REMOTE_ROOT_ARG $REMOTE_STAGE_ARG $REMOTE_USER_ARG $REMOTE_REVISION_ARG $REMOTE_DEPLOY_ID_ARG $REMOTE_TIMEOUT_ARG" <<'REMOTE_DEPLOY'
#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

ROOT="$1"
STAGE="$2"
REMOTE_USER="$3"
REVISION="$4"
DEPLOY_ID="$5"
HEALTH_TIMEOUT="$6"

BOT_LABEL="dev.life-ustc.bot"
RENDERD_LABEL="dev.life-ustc.renderd"
BOT_BINARY="$ROOT/bin/life-ustc-bot"
RENDERD_BINARY="$ROOT/bin/renderd"
DB_PATH="$ROOT/data/life-ustc-bot.db"
FONT_DIR="$ROOT/runtime-fonts"
LAUNCHD_DIR="${DEPLOY_LAUNCHD_DIR:-/Library/LaunchDaemons}"
BOT_PLIST="$LAUNCHD_DIR/$BOT_LABEL.plist"
RENDERD_PLIST="$LAUNCHD_DIR/$RENDERD_LABEL.plist"
PYTHON="${DEPLOY_PYTHON:-/opt/homebrew/bin/python3}"
CURL="${DEPLOY_CURL:-/usr/bin/curl}"
FILE="${DEPLOY_FILE_COMMAND:-/usr/bin/file}"
SUDO=(sudo -n)
CARGO_TARGET_DIR="$ROOT/build/cargo-target"
CARGO_BUILD_LOCK="$ROOT/build/.cargo-build.lock"

exec 3>&1
mkdir -p "$STAGE/diagnostics"
chmod 700 "$STAGE" "$STAGE/diagnostics"
: >"$STAGE/deploy.log"
chmod 600 "$STAGE/deploy.log"
exec >"$STAGE/deploy.log" 2>&1

log() {
	printf '%s\n' "$*" >&3
}

fail() {
	echo "$*" >&2
	log "deployment step failed: $*"
	return 1
}

[[ "$REMOTE_USER" =~ ^[A-Za-z0-9._-]+$ ]] || fail "remote launchd user contains unsupported characters"

sudo_cmd() {
	"${SUDO[@]}" "$@"
}

loaded() {
	local label="$1"
	sudo_cmd launchctl print "system/$label" >/dev/null 2>&1
}

bootout_if_loaded() {
	local label="$1"
	if loaded "$label"; then
		sudo_cmd launchctl bootout "system/$label" || return 1
		if loaded "$label"; then
			return 1
		fi
	fi
	return 0
}

bootstrap() {
	local plist="$1"
	sudo_cmd launchctl bootstrap system "$plist"
	sudo_cmd launchctl kickstart -k "system/$(basename "$plist" .plist)"
}

wait_for_http() {
	local url="$1"
	local description="$2"
	local deadline=$((SECONDS + HEALTH_TIMEOUT))
	while (( SECONDS < deadline )); do
		if "$CURL" --fail --silent --show-error --max-time 3 "$url" >/dev/null 2>&1; then
			log "$description is healthy"
			return 0
		fi
		sleep 1
	done
	fail "$description did not become healthy within ${HEALTH_TIMEOUT}s"
}

copy_binary_backup() {
	local source="$1"
	local target="$2"
	[[ -f "$source" && ! -L "$source" ]] || fail "missing live binary: $source"
	sudo_cmd cp -p "$source" "$target"
}

install_binary_atomically() {
	local staged="$1"
	local target="$2"
	local temporary="$target.$DEPLOY_ID.tmp"
	sudo_cmd install -m 755 "$staged" "$temporary"
	sudo_cmd mv -f "$temporary" "$target"
}

install_plist_atomically() {
	local staged="$1"
	local target="$2"
	local temporary="$LAUNCHD_DIR/.$(basename "$target").$DEPLOY_ID.tmp"
	sudo_cmd install -o root -g wheel -m 600 "$staged" "$temporary"
	sudo_cmd mv -f "$temporary" "$target"
}

restore_plist() {
	local backup="$1"
	local target="$2"
	local temporary="$LAUNCHD_DIR/.$(basename "$target").$DEPLOY_ID.rollback.tmp"
	sudo_cmd install -o root -g wheel -m 600 "$backup" "$temporary"
	sudo_cmd mv -f "$temporary" "$target"
}

backup_database() {
	"$PYTHON" - "$DB_PATH" "$ROLLBACK/database" <<'PY'
import os
import sqlite3
import sys

source_path, destination_path = sys.argv[1:]
temporary_path = destination_path + ".tmp"
try:
    if os.path.exists(temporary_path):
        os.unlink(temporary_path)
    source = sqlite3.connect(source_path)
    destination = sqlite3.connect(temporary_path)
    try:
        source.backup(destination)
        destination.commit()
    finally:
        destination.close()
        source.close()
    os.replace(temporary_path, destination_path)
except Exception:
    try:
        os.unlink(temporary_path)
    except FileNotFoundError:
        pass
    raise
PY
}

restore_database() {
	"$PYTHON" - "$DB_PATH" "$ROLLBACK/database" <<'PY'
import os
import shutil
import sys

destination_path, source_path = sys.argv[1:]
for suffix in ("-wal", "-shm"):
    try:
        os.unlink(destination_path + suffix)
    except FileNotFoundError:
        pass
temporary_path = destination_path + ".restore.tmp"
shutil.copy2(source_path, temporary_path)
os.replace(temporary_path, destination_path)
PY
}

release_cargo_build_lock() {
	if [[ -d "$CARGO_BUILD_LOCK" ]]; then
		rm -f "$CARGO_BUILD_LOCK/deployment-id"
		rmdir "$CARGO_BUILD_LOCK" 2>/dev/null || true
	fi
}

write_plists() {
	"$PYTHON" - "$ROOT/config.json" "$STAGE/bot.plist" "$STAGE/renderd.plist" "$ROOT" "$REVISION" "$REMOTE_USER" <<'PY'
import json
import os
import plistlib
import re
import sys

config_path, bot_path, renderd_path, root, revision, remote_user = sys.argv[1:]
key_pattern = re.compile(r"[A-Z][A-Z0-9_]*$")
user_pattern = re.compile(r"[A-Za-z0-9._-]+$")
if not user_pattern.fullmatch(remote_user):
    raise ValueError("remote launchd user contains unsupported characters")

def reject_duplicate_keys(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("config.json contains a duplicate key")
        result[key] = value
    return result

with open(config_path, "rb") as stream:
    config = json.load(stream, object_pairs_hook=reject_duplicate_keys)
if not isinstance(config, dict):
    raise ValueError("config.json must contain an object")

environment = {}
for key, value in config.items():
    if not isinstance(key, str) or not key_pattern.fullmatch(key):
        raise ValueError("config.json contains an invalid environment key")
    if isinstance(value, bool):
        value = "true" if value else "false"
    elif isinstance(value, (int, float, str)):
        value = str(value)
    else:
        raise ValueError("config.json environment values must be strings or numbers")
    if "\x00" in value or "\n" in value or "\r" in value:
        raise ValueError("config.json contains an environment value with a control character")
    environment[key] = value

# These values belong to the deployment transaction and cannot be overridden
# by the private config file.
environment.update({
    "BOT_DB_PATH": os.path.join(root, "data", "life-ustc-bot.db"),
    "BOT_RENDER_ENDPOINT": "http://127.0.0.1:9123/render",
    "BOT_BUILD_VERSION": revision,
    "BOT_HEALTH_ADDR": "127.0.0.1:2282",
})

def write(path, label, arguments, env, stdout, stderr):
    document = {
        "Label": label,
        "ProgramArguments": arguments,
        "WorkingDirectory": root,
        "UserName": remote_user,
        "RunAtLoad": True,
        "KeepAlive": True,
        "EnvironmentVariables": env,
        "StandardOutPath": os.path.join(root, "logs", stdout),
        "StandardErrorPath": os.path.join(root, "logs", stderr),
    }
    with open(path, "wb") as stream:
        plistlib.dump(document, stream, fmt=plistlib.FMT_XML, sort_keys=False)

write(bot_path, "dev.life-ustc.bot", [os.path.join(root, "bin", "life-ustc-bot")], environment,
      "bot.log", "bot.error.log")
write(renderd_path, "dev.life-ustc.renderd", [os.path.join(root, "bin", "renderd")], {
    "RENDERD_ADDR": "127.0.0.1:9123",
    "RENDERD_FONT_DIR": os.path.join(root, "runtime-fonts"),
    "RENDERD_SCALE": "3",
}, "renderd.log", "renderd.error.log")
PY
}

rollback() {
	local original_status="$1"
	local rollback_status=0
	set +e
	log "deployment failed; restoring the previous binaries, database, and launchd jobs"
	if ! bootout_if_loaded "$BOT_LABEL"; then
		log "rollback aborted: could not ensure the bot was stopped; database and binaries were left in place"
		return 1
	fi
	if ! bootout_if_loaded "$RENDERD_LABEL"; then
		log "rollback aborted: could not ensure renderd was stopped; database and binaries were left in place"
		return 1
	fi
	if [[ -f "$ROLLBACK/database" ]]; then
		restore_database || rollback_status=1
	fi
	if [[ -f "$ROLLBACK/life-ustc-bot" ]]; then
		install_binary_atomically "$ROLLBACK/life-ustc-bot" "$BOT_BINARY" || rollback_status=1
	fi
	if [[ -f "$ROLLBACK/renderd" ]]; then
		install_binary_atomically "$ROLLBACK/renderd" "$RENDERD_BINARY" || rollback_status=1
	fi
	sudo_cmd rm -f "$BOT_PLIST" "$RENDERD_PLIST" || rollback_status=1
	if [[ -f "$ROLLBACK/bot.plist" ]]; then
		restore_plist "$ROLLBACK/bot.plist" "$BOT_PLIST" || rollback_status=1
	fi
	if [[ -f "$ROLLBACK/renderd.plist" ]]; then
		restore_plist "$ROLLBACK/renderd.plist" "$RENDERD_PLIST" || rollback_status=1
	fi
	if (( RENDERD_WAS_LOADED == 1 )); then
		bootstrap "$RENDERD_PLIST" || rollback_status=1
	fi
	if (( BOT_WAS_LOADED == 1 )); then
		bootstrap "$BOT_PLIST" || rollback_status=1
	fi
	if (( rollback_status != 0 )); then
		log "rollback did not complete; inspect $STAGE/deploy.log"
		return 1
	fi
	log "rollback complete"
	return "$original_status"
}

release_lock() {
	if [[ -d "$LOCK_DIR" ]]; then
		rm -f "$LOCK_DIR/deployment-id"
		rmdir "$LOCK_DIR" 2>/dev/null || true
	fi
}

on_exit() {
	local status=$?
	if (( DEPLOY_SUCCEEDED == 0 )); then
		rollback "$status" || status=$?
	fi
	release_lock
	exit "$status"
}

[[ "$(uname -s)" == Darwin ]] || fail "remote host is not Darwin"
[[ "$(uname -m)" == arm64 ]] || fail "remote host is not arm64"
[[ -x "$PYTHON" ]] || fail "missing required remote Python: $PYTHON"
[[ -d "$FONT_DIR" && ! -L "$FONT_DIR" ]] || fail "missing runtime font directory: $FONT_DIR"
[[ -f "$ROOT/config.json" && ! -L "$ROOT/config.json" ]] || fail "missing remote config.json"

export PATH="$ROOT/toolchain/go/bin:$ROOT/toolchain/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
export CGO_ENABLED=1
command -v go >/dev/null 2>&1 || fail "missing remote Go compiler"
command -v cargo >/dev/null 2>&1 || fail "missing remote Rust compiler"
[[ "$(go env GOOS)" == darwin && "$(go env GOARCH)" == arm64 ]] || fail "remote Go compiler is not configured for darwin/arm64"

# Both builds use only dependency material already present in the staged
# source or the target's local Cargo cache.  This keeps a deployment tied to
# the committed go.sum/Cargo.lock instead of allowing a remote dependency
# update during the transaction.
(cd "$STAGE/source" && go build -mod=vendor -trimpath -ldflags='-s -w' -o "$STAGE/bin/life-ustc-bot" ./cmd/life-ustc-bot)
[[ ! -L "$CARGO_TARGET_DIR" ]] || fail "Cargo target cache is a symlink"
[[ ! -L "$CARGO_BUILD_LOCK" ]] || fail "Cargo build lock is a symlink"
mkdir "$CARGO_BUILD_LOCK" || fail "another Cargo build is already using the target cache"
printf '%s\n' "$DEPLOY_ID" >"$CARGO_BUILD_LOCK/deployment-id"
trap release_cargo_build_lock EXIT
CARGO_NET_OFFLINE=true CARGO_TARGET_DIR="$CARGO_TARGET_DIR" cargo build \
	--manifest-path "$STAGE/source/renderd/Cargo.toml" \
	--release --locked --offline
cp -- "$CARGO_TARGET_DIR/release/renderd" "$STAGE/bin/renderd"
release_cargo_build_lock
trap - EXIT
chmod 755 "$STAGE/bin/life-ustc-bot" "$STAGE/bin/renderd"
[[ "$("$FILE" "$STAGE/bin/life-ustc-bot")" == *"Mach-O 64-bit executable arm64"* ]] || fail "built bot is not a darwin/arm64 executable"
[[ "$("$FILE" "$STAGE/bin/renderd")" == *"Mach-O 64-bit executable arm64"* ]] || fail "built renderd is not a darwin/arm64 executable"

[[ -f "$DB_PATH" && ! -L "$DB_PATH" ]] || fail "missing remote SQLite database: $DB_PATH"
[[ -f "$BOT_BINARY" && ! -L "$BOT_BINARY" ]] || fail "missing live bot binary: $BOT_BINARY"
[[ -f "$RENDERD_BINARY" && ! -L "$RENDERD_BINARY" ]] || fail "missing live renderd binary: $RENDERD_BINARY"
[[ -f "$BOT_PLIST" && ! -L "$BOT_PLIST" ]] || fail "missing live bot plist: $BOT_PLIST"
[[ -f "$RENDERD_PLIST" && ! -L "$RENDERD_PLIST" ]] || fail "missing live renderd plist: $RENDERD_PLIST"

LOCK_DIR="$ROOT/build/.deploy.lock"
[[ ! -L "$LOCK_DIR" ]] || fail "deployment lock path is a symlink"
mkdir "$LOCK_DIR" || fail "another deployment is already running"
printf '%s\n' "$DEPLOY_ID" >"$LOCK_DIR/deployment-id"
chmod 700 "$LOCK_DIR"
DEPLOY_SUCCEEDED=0
BOT_WAS_LOADED=0
RENDERD_WAS_LOADED=0
ROLLBACK="$STAGE/rollback"
trap release_lock EXIT

if loaded "$BOT_LABEL"; then BOT_WAS_LOADED=1; fi
if loaded "$RENDERD_LABEL"; then RENDERD_WAS_LOADED=1; fi
copy_binary_backup "$BOT_BINARY" "$ROLLBACK/life-ustc-bot"
copy_binary_backup "$RENDERD_BINARY" "$ROLLBACK/renderd"
sudo_cmd cp -p "$BOT_PLIST" "$ROLLBACK/bot.plist"
sudo_cmd cp -p "$RENDERD_PLIST" "$ROLLBACK/renderd.plist"
trap on_exit EXIT

# Quiesce the bot before taking the SQLite snapshot and running its migration.
# renderd is also stopped before its binary is replaced, so launchd cannot
# retain an old process while the new plist is installed.
bootout_if_loaded "$BOT_LABEL"
bootout_if_loaded "$RENDERD_LABEL"
backup_database

write_plists
[[ -f "$STAGE/bot.plist" && -f "$STAGE/renderd.plist" ]] || fail "plist generation did not produce both files"

BOT_DB_PATH="$DB_PATH" "$STAGE/bin/life-ustc-bot" migrate >"$STAGE/diagnostics/migrate.log" 2>&1 || fail "database migration failed"

# Both renames happen on the same filesystem and replace the live paths in one
# operation.  The copies in rollback/ make either partial replacement safe.
install_binary_atomically "$STAGE/bin/life-ustc-bot" "$BOT_BINARY"
install_binary_atomically "$STAGE/bin/renderd" "$RENDERD_BINARY"
install_plist_atomically "$STAGE/bot.plist" "$BOT_PLIST"
install_plist_atomically "$STAGE/renderd.plist" "$RENDERD_PLIST"

bootstrap "$RENDERD_PLIST"
wait_for_http "http://127.0.0.1:9123/healthz" "renderd"
bootstrap "$BOT_PLIST"
wait_for_http "http://127.0.0.1:2282/live" "bot"

DEPLOY_SUCCEEDED=1
log "deployment succeeded: revision $REVISION"
exit 0
REMOTE_DEPLOY
