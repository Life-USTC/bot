#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
remote_script="$(mktemp "${TMPDIR:-/tmp}/deploy-mac-remote.XXXXXXXX")"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/deploy-mac-test.XXXXXXXX")"
test_phase="initialization"
cleanup() {
	rm -f "$remote_script"
	rm -rf -- "$fixture"
}
debug_file() {
	local path="$1"
	if [[ -f "$path" ]]; then
		sed 's/secret-value/[redacted]/g' "$path"
	fi
}
on_error() {
	local status=$?
	trap - ERR
	printf 'deploy-mac smoke test failed: phase=%s status=%s command=%s\n' \
		"$test_phase" "$status" "$BASH_COMMAND" >&2
	for output in "${success_output:-}" "${failure_output:-}" "${blocked_output:-}"; do
		if [[ -n "$output" && -f "$output" ]]; then
			printf '%s:\n' "$output" >&2
			debug_file "$output" >&2
		fi
	done
	for detail in "$fixture"/*/root/build/test/deploy.log; do
		debug_file "$detail" >&2
	done
	exit "$status"
}
trap on_error ERR
trap cleanup EXIT

test_phase="shell syntax and remote script extraction"
bash -n "$root/scripts/deploy-mac.sh"
awk '/^ssh .*<<.*REMOTE_DEPLOY/ {capture=1; next} capture && /^REMOTE_DEPLOY$/ {exit} capture {print}' \
	"$root/scripts/deploy-mac.sh" >"$remote_script"
bash -n "$remote_script"

# The remote config is consumed by Python's JSON parser. It must never be
# sourced as shell code, and a dry run must not contact SSH or print values.
test_phase="dry-run and config-source checks"
! grep -E '(^|[[:space:]])(source|\.)[[:space:]].*config\.json' "$root/scripts/deploy-mac.sh" >/dev/null
dry_run_output="$(DEPLOY_DRY_RUN=1 REMOTE_HOST=tkm-mac-mini "$root/scripts/deploy-mac.sh" 2>&1)"
grep -F 'dry-run would deploy revision ' <<<"$dry_run_output" >/dev/null
grep -F 'performs no SSH, source, binary, database, or launchd changes' <<<"$dry_run_output" >/dev/null

mock_launchctl() {
	local operation="${1:-}"
	shift || true
	local label
	case "$operation" in
		print)
			label="${1#system/}"
			[[ -f "$DEPLOY_TEST_FIXTURE/$label" ]]
			;;
		bootout)
			label="${1#system/}"
			if [[ "$label" == dev.life-ustc.bot && "${DEPLOY_TEST_FAIL_ROLLBACK_BOT_BOOTOUT:-0}" == 1 && -f "$DEPLOY_TEST_FIXTURE/trigger-rollback" ]]; then
				return 1
			fi
			rm -f "$DEPLOY_TEST_FIXTURE/$label"
			;;
		bootstrap)
			[[ "${1:-}" == system ]]
			label="$(basename "$2" .plist)"
			: >"$DEPLOY_TEST_FIXTURE/$label"
			;;
		kickstart)
			return 0
			;;
		*)
			return 2
			;;
	esac
}

sudo() {
	if [[ "${1:-}" == -n ]]; then
		shift
	fi
	local operation="${1:-}"
	shift || true
	if [[ "$operation" == launchctl ]]; then
		mock_launchctl "$@"
		return
	fi
	if [[ "$operation" == install ]]; then
		local -a args=()
		while (($# > 0)); do
			case "$1" in
				-o|-g)
					shift 2
					;;
				-m)
					args+=("$1" "$2")
					shift 2
					;;
				*)
					args+=("$1")
					shift
					;;
			esac
		done
		command install "${args[@]}"
		return
	fi
	command "$operation" "$@"
}

uname() {
	case "${1:-}" in
		-s) printf 'Darwin\n' ;;
		-m) printf 'arm64\n' ;;
		*) command uname "$@" ;;
	esac
}

go() {
	if [[ "${1:-}" == env ]]; then
		case "${2:-}" in
			GOOS) printf 'darwin\n' ;;
			GOARCH) printf 'arm64\n' ;;
			*) return 1 ;;
		esac
		return
	fi
	[[ "${1:-}" == build ]]
	local output=""
	while (($# > 0)); do
		if [[ "$1" == -o ]]; then
			output="$2"
			shift 2
		else
			shift
		fi
	done
	[[ -n "$output" ]]
	printf '%s\n' \
		'#!/bin/sh' \
		'if [ "$1" = migrate ]; then' \
		'  python3 -c "import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); c.execute(\"DELETE FROM marker\"); c.execute(\"INSERT INTO marker(value) VALUES (\\\"new database\\\")\"); c.commit(); c.close()" "$BOT_DB_PATH"' \
		'  exit 0' \
		'fi' \
		'echo mock-bot' >"$output"
	chmod 755 "$output"
}

cargo() {
	[[ "${CARGO_TARGET_DIR:-}" == "$DEPLOY_TEST_ROOT/build/cargo-target" ]]
	mkdir -p "$CARGO_TARGET_DIR/release"
	printf '%s\n' '#!/bin/sh' 'echo mock-renderd' >"$CARGO_TARGET_DIR/release/renderd"
	chmod 755 "$CARGO_TARGET_DIR/release/renderd"
}

file() {
	printf '%s: Mach-O 64-bit executable arm64\n' "$1"
}

curl() {
	local url="${*: -1}"
	if [[ "${DEPLOY_TEST_FAIL_BOT_HEALTH:-0}" == 1 && "$url" == *127.0.0.1:2282/* ]]; then
		: >"$DEPLOY_TEST_FIXTURE/trigger-rollback"
		return 1
	fi
	return 0
}

export -f sudo mock_launchctl uname go cargo file curl

prepare_fixture() {
	local name="$1"
	local test_root="$fixture/$name/root"
	local launchd="$fixture/$name/LaunchDaemons"
	local stage="$test_root/build/test"
	mkdir -p "$test_root"/{bin,data,logs,build,runtime-fonts} \
		"$stage"/{source/cmd,source/renderd,bin,rollback,diagnostics} \
		"$launchd" "$fixture/$name"
	printf '%s\n' old-bot >"$test_root/bin/life-ustc-bot"
	printf '%s\n' old-renderd >"$test_root/bin/renderd"
	chmod 755 "$test_root/bin/life-ustc-bot" "$test_root/bin/renderd"
	printf '%s\n' '{"LIFE_USTC_SERVER":"https://example.invalid","OPENAI_API_KEY":"secret-value"}' >"$test_root/config.json"
	printf '%s\n' old-bot-plist >"$launchd/dev.life-ustc.bot.plist"
	printf '%s\n' old-renderd-plist >"$launchd/dev.life-ustc.renderd.plist"
	: >"$fixture/$name/dev.life-ustc.bot"
	: >"$fixture/$name/dev.life-ustc.renderd"
	python3 - "$test_root/data/life-ustc-bot.db" <<'PY'
import sqlite3
import sys

connection = sqlite3.connect(sys.argv[1])
try:
    connection.execute("CREATE TABLE marker (value TEXT NOT NULL)")
    connection.execute("INSERT INTO marker(value) VALUES ('old database')")
    connection.commit()
finally:
    connection.close()
PY
	printf '%s\n' placeholder >"$stage/source/go.mod"
	printf '%s\n' placeholder >"$stage/source/renderd/Cargo.toml"
	printf '%s\n' placeholder >"$stage/source/renderd/Cargo.lock"
	printf '%s\n' "$test_root|$launchd|$stage"
}

run_remote() {
	local test_root="$1"
	local launchd="$2"
	local stage="$3"
	local remote_user="$4"
	local output="$5"
	shift 5
	set +e
	DEPLOY_TEST_FIXTURE="$(dirname "$launchd")" \
	DEPLOY_TEST_ROOT="$test_root" \
	DEPLOY_LAUNCHD_DIR="$launchd" \
	DEPLOY_PYTHON="$(command -v python3)" \
	DEPLOY_CURL=curl \
	DEPLOY_FILE_COMMAND=file \
	env "$@" bash "$remote_script" "$test_root" "$stage" \
		"$remote_user" 01234567890123456789012345678901234567890123 test-deployment 1 \
		>"$output" 2>&1
	local status=$?
	set -e
	return "$status"
}

test_phase="successful deployment transaction"
success_fixture="$(prepare_fixture success)"
IFS='|' read -r success_root success_launchd success_stage <<<"$success_fixture"
success_output="$fixture/success-output"
run_remote "$success_root" "$success_launchd" "$success_stage" deploy-account "$success_output"
grep -F 'deployment succeeded: revision ' "$success_output" >/dev/null
[[ -f "$fixture/success/dev.life-ustc.bot" ]]
[[ -f "$fixture/success/dev.life-ustc.renderd" ]]
grep -F 'mock-bot' "$success_root/bin/life-ustc-bot" >/dev/null
grep -F 'mock-renderd' "$success_root/bin/renderd" >/dev/null
! grep -F 'secret-value' "$success_output" >/dev/null
grep -F 'secret-value' "$success_launchd/dev.life-ustc.bot.plist" >/dev/null
python3 - "$success_launchd/dev.life-ustc.bot.plist" "$success_launchd/dev.life-ustc.renderd.plist" <<'PY'
import plistlib
import os
import stat
import sys

bot_path, renderd_path = sys.argv[1:]
with open(bot_path, "rb") as stream:
    bot = plistlib.load(stream)
with open(renderd_path, "rb") as stream:
    renderd = plistlib.load(stream)
assert bot["Label"] == "dev.life-ustc.bot"
assert bot["UserName"] == "deploy-account"
assert bot["RunAtLoad"] and bot["KeepAlive"]
assert bot["EnvironmentVariables"]["BOT_RENDER_ENDPOINT"] == "http://127.0.0.1:9123/render"
assert bot["EnvironmentVariables"]["BOT_BUILD_VERSION"].startswith("0123456789")
assert bot["WorkingDirectory"] == bot["EnvironmentVariables"]["BOT_DB_PATH"].removesuffix("/data/life-ustc-bot.db")
assert bot["StandardOutPath"].endswith("/logs/bot.log")
assert bot["StandardErrorPath"].endswith("/logs/bot.error.log")
assert renderd["Label"] == "dev.life-ustc.renderd"
assert renderd["UserName"] == "deploy-account"
assert renderd["RunAtLoad"] and renderd["KeepAlive"]
assert renderd["EnvironmentVariables"]["RENDERD_ADDR"] == "127.0.0.1:9123"
assert renderd["EnvironmentVariables"]["RENDERD_FONT_DIR"].endswith("/runtime-fonts")
assert renderd["EnvironmentVariables"]["RENDERD_SCALE"] == "3"
assert stat.S_IMODE(os.stat(bot_path).st_mode) == 0o600
assert stat.S_IMODE(os.stat(renderd_path).st_mode) == 0o600
PY

test_phase="rollback after health failure"
failure_fixture="$(prepare_fixture failure)"
IFS='|' read -r failure_root failure_launchd failure_stage <<<"$failure_fixture"
failure_output="$fixture/failure-output"
if run_remote "$failure_root" "$failure_launchd" "$failure_stage" tiankaima "$failure_output" \
	DEPLOY_TEST_FAIL_BOT_HEALTH=1; then
	echo "expected mocked bot health failure" >&2
	exit 1
fi
grep -F 'rollback complete' "$failure_output" >/dev/null
grep -F old-bot "$failure_root/bin/life-ustc-bot" >/dev/null
grep -F old-renderd "$failure_root/bin/renderd" >/dev/null
grep -F old-bot-plist "$failure_launchd/dev.life-ustc.bot.plist" >/dev/null
grep -F old-renderd-plist "$failure_launchd/dev.life-ustc.renderd.plist" >/dev/null
python3 - "$failure_root/data/life-ustc-bot.db" <<'PY'
import sqlite3
import sys

connection = sqlite3.connect(sys.argv[1])
try:
    assert connection.execute("SELECT value FROM marker").fetchone()[0] == "old database"
finally:
    connection.close()
PY
! grep -F 'secret-value' "$failure_output" >/dev/null

test_phase="rollback stop-safety failure"
blocked_fixture="$(prepare_fixture blocked)"
IFS='|' read -r blocked_root blocked_launchd blocked_stage <<<"$blocked_fixture"
blocked_output="$fixture/blocked-output"
if run_remote "$blocked_root" "$blocked_launchd" "$blocked_stage" tiankaima "$blocked_output" \
	DEPLOY_TEST_FAIL_BOT_HEALTH=1 DEPLOY_TEST_FAIL_ROLLBACK_BOT_BOOTOUT=1; then
	echo "expected mocked rollback stop failure" >&2
	exit 1
fi
grep -F 'rollback aborted: could not ensure the bot was stopped' "$blocked_output" >/dev/null
! grep -F 'rollback complete' "$blocked_output" >/dev/null
[[ -f "$fixture/blocked/dev.life-ustc.bot" ]]
grep -F 'mock-bot' "$blocked_root/bin/life-ustc-bot" >/dev/null
python3 - "$blocked_root/data/life-ustc-bot.db" <<'PY'
import sqlite3
import sys

connection = sqlite3.connect(sys.argv[1])
try:
    assert connection.execute("SELECT value FROM marker").fetchone()[0] == "new database"
finally:
    connection.close()
PY
! grep -F 'secret-value' "$blocked_output" >/dev/null

echo "deploy-mac smoke test passed"
