#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
remote_script="$(mktemp)"
parser_script="$(mktemp)"
parser_env="$(mktemp)"
fixture=""
cleanup() {
	rm -f "$remote_script" "$parser_script" "$parser_env"
	if [[ -n "$fixture" ]]; then
		rm -rf "$fixture"
	fi
}
trap cleanup EXIT

bash -n "$root/scripts/deploy-cn.sh"
awk '/^ssh .*REMOTE_DEPLOY.*/ {capture=1; next} capture && /^REMOTE_DEPLOY$/ {exit} capture {print}' \
	"$root/scripts/deploy-cn.sh" >"$remote_script"
bash -n "$remote_script"
! grep -F 'compose_stage build' "$remote_script" >/dev/null

# Extract and execute the parser heredoc itself. This catches indentation or
# quoting regressions that a shell-only syntax check cannot see.
awk '
    /^parse_db_container_path\(\) \{$/ { state = 1; next }
    state == 1 && /^python3 - / { state = 2; next }
    state == 2 && /^PY$/ { exit }
    state == 2 { print }
' "$root/scripts/deploy-cn.sh" >"$parser_script"
printf 'SECRET=not-printed\nexport BOT_DB_PATH="/data/test.db"\n' >"$parser_env"
[[ "$(python3 "$parser_script" "$parser_env")" == /data/test.db ]]

# A failed migration gate must leave the live source untouched and must not
# start the new service. The mocked Compose/Docker functions still allow the
# rollback path to restart the old service, which is the safe outcome.
fixture="$(mktemp -d)"
test_root="$fixture/root"
test_stage="$test_root/.deploy-staging/test"
test_rollback="$test_root/.deploy-rollback/test"
mkdir -p "$test_root/data" "$test_stage/src" "$test_stage/diagnostics" "$test_rollback"
printf 'old source\n' >"$test_root/src.marker"
mkdir -p "$test_root/src"
printf 'old source\n' >"$test_root/src/version"
mkdir -p "$test_stage/src"
printf 'new source\n' >"$test_stage/src/version"
printf 'SECRET=not-printed\nBOT_DB_PATH=/data/life-ustc-bot.db\nBOT_BUILD_VERSION=old\nexport BOT_BUILD_VERSION=older\n' >"$test_root/.env"
cp -- "$test_root/.env" "$test_stage/.env"
printf 'services:\n  bot:\n    image: old\n' >"$test_root/compose.yaml"
printf 'services:\n  bot:\n    image: new\n  renderd:\n    image: new-renderd\n' >"$test_stage/compose.yaml"
printf 'old-revision\n' >"$test_root/.deploy-revision"
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

export DEPLOY_TEST_FIXTURE="$fixture"
export DEPLOY_TEST_PHASE=running
export DEPLOY_TEST_OLD_CONTAINER=0123456789abcdef
export DEPLOY_TEST_OLD_IMAGE=sha256:$(printf '1%.0s' {1..64})
export DEPLOY_TEST_OLD_IMAGE_REFERENCE=life-ustc-bot:0123456789012345678901234567890123456789
export DEPLOY_TEST_NEW_IMAGE=sha256:$(printf '2%.0s' {1..64})
docker() {
	local command="${1:-}"
	if [[ "$command" == compose ]]; then
		local arguments="$*"
		if [[ "$arguments" == *" config --services"* ]]; then
			if [[ "$arguments" == *".deploy-staging/"* ]]; then
				if [[ "${DEPLOY_TEST_MISSING_RENDERD:-0}" == 1 ]]; then
					printf 'bot\n'
				else
					printf 'bot\nrenderd\n'
				fi
			else
				printf 'bot\n'
			fi
			return 0
		fi
		if [[ "$arguments" == *" config "* ]]; then
			return 0
		fi
		if [[ "$arguments" == *" build "* || "$arguments" == *" build" ]]; then
			[[ -f "$DEPLOY_TEST_FIXTURE/rollback-image-tagged" ]] || return 43
			: >"$DEPLOY_TEST_FIXTURE/build-ran"
			return 0
		fi
		if [[ "$arguments" == *" stop "* ]]; then
			: >"$DEPLOY_TEST_FIXTURE/service-stopped"
			DEPLOY_TEST_PHASE=stopped
			return 0
		fi
		if [[ "$arguments" == *" logs "* ]]; then
			return 0
		fi
		if [[ "$arguments" == *" ps -aq "* || "$arguments" == *" ps -q "* ]]; then
			printf '%s\n' "$DEPLOY_TEST_OLD_CONTAINER"
			return 0
		fi
		if [[ "$arguments" == *" up "* ]]; then
			if [[ "$arguments" == *".deploy-rollback/"* ]]; then
				: >"$DEPLOY_TEST_FIXTURE/rollback-started"
			else
				: >"$DEPLOY_TEST_FIXTURE/active-started"
			fi
			DEPLOY_TEST_PHASE=running
			return 0
		fi
		return 0
	fi
	if [[ "$command" == load ]]; then
		[[ -f "$DEPLOY_TEST_FIXTURE/rollback-image-tagged" ]] || return 43
		: >"$DEPLOY_TEST_FIXTURE/images-loaded"
		[[ "${DEPLOY_TEST_FAIL_LOAD:-0}" != 1 ]]
	fi
	if [[ "$command" == image && "${2:-}" == inspect ]]; then
		if [[ "${3:-}" == --format ]]; then
			case "${5:-}" in
				life-ustc-bot:rollback-*) printf '%s\n' "$DEPLOY_TEST_OLD_IMAGE" ;;
				life-ustc-bot:0123456789012345678901234567890123456789) printf '%s\n' "$DEPLOY_TEST_NEW_IMAGE" ;;
				*) printf '%s\n' "$DEPLOY_TEST_NEW_IMAGE" ;;
			esac
		else
			printf '%s\n' "${3:-$DEPLOY_TEST_OLD_IMAGE}"
		fi
		return 0
	fi
	if [[ "$command" == inspect ]]; then
		local format="${3:-}"
		local target="${4:-}"
		case "$format" in
			*State.Running*) [[ "$DEPLOY_TEST_PHASE" == running ]] && printf 'true\n' || printf 'false\n' ;;
			*State.Health*) printf 'healthy\n' ;;
			*State.Status*) printf 'running\n' ;;
			*'.Config.Image'*) printf '%s\n' "$DEPLOY_TEST_OLD_IMAGE_REFERENCE" ;;
			*'.Image'*) printf '%s\n' "$DEPLOY_TEST_OLD_IMAGE" ;;
			*) printf '\n' ;;
		esac
		return 0
	fi
	if [[ "$command" == tag ]]; then
		if [[ "${DEPLOY_TEST_FAIL_TAG:-0}" == 1 ]]; then
			return 44
		fi
		[[ "${2:-}" == "$DEPLOY_TEST_OLD_IMAGE" ]]
		[[ ! -f "$DEPLOY_TEST_FIXTURE/images-loaded" ]]
		: >"$DEPLOY_TEST_FIXTURE/rollback-image-tagged"
		return 0
	fi
	if [[ "$command" == stop ]]; then
		return 0
	fi
	if [[ "$command" == run ]]; then
		: >"$DEPLOY_TEST_FIXTURE/migration-ran"
		return 42
	fi
	return 0
}
chown() { return 0; }
export -f docker chown
: >"$test_stage/images.tar"
set +e
	bash "$remote_script" "$test_root" "$test_stage" "$test_rollback" \
		0123456789012345678901234567890123456789 bot 1 life-ustc-bot life-ustc-renderd "${DEPLOY_TEST_EXPECTED_IMAGE:-$DEPLOY_TEST_NEW_IMAGE}" "$DEPLOY_TEST_NEW_IMAGE" test \
	>"$fixture/output" 2>&1
failure_status=$?
set -e
[[ "$failure_status" -ne 0 ]]
[[ -f "$fixture/rollback-image-tagged" ]]
[[ ! -f "$fixture/build-ran" ]]
[[ -f "$fixture/migration-ran" ]]
[[ ! -f "$fixture/active-started" ]]
[[ -f "$fixture/rollback-started" ]]
grep -F 'old source' "$test_root/src/version" >/dev/null
grep -F 'old-revision' "$test_root/.deploy-revision" >/dev/null
grep -F 'SECRET=not-printed' "$test_root/.env" >/dev/null
[[ "$(grep -Ec '^(export[[:space:]]+)?BOT_BUILD_VERSION=' "$test_stage/.env")" -eq 1 ]]
grep -Fx 'BOT_BUILD_VERSION=0123456789012345678901234567890123456789' "$test_stage/.env" >/dev/null
python3 - "$test_root/data/life-ustc-bot.db" <<'PY'
import sqlite3
import sys

connection = sqlite3.connect(sys.argv[1])
try:
    assert connection.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
    assert connection.execute("SELECT value FROM marker").fetchone()[0] == "old database"
finally:
    connection.close()
PY
! grep -F 'not-printed' "$fixture/output" >/dev/null

# A staged Compose file that omits the renderd service must fail before any
# image preservation, stop, or database operation is attempted.
validation_root="$fixture/validation-root"
validation_stage="$validation_root/.deploy-staging/validation"
validation_rollback="$validation_root/.deploy-rollback/validation"
mkdir -p "$validation_root/data" "$validation_stage/src" "$validation_stage/diagnostics" "$validation_rollback"
printf 'SECRET=not-printed\nBOT_DB_PATH=/data/life-ustc-bot.db\n' >"$validation_stage/.env"
printf 'services:\n  bot:\n    image: new\n' >"$validation_stage/compose.yaml"
export DEPLOY_TEST_FIXTURE="$fixture/validation"
mkdir -p "$DEPLOY_TEST_FIXTURE"
export DEPLOY_TEST_MISSING_RENDERD=1
: >"$validation_stage/images.tar"
set +e
bash "$remote_script" "$validation_root" "$validation_stage" "$validation_rollback" \
	0123456789012345678901234567890123456789 bot 1 life-ustc-bot life-ustc-renderd "${DEPLOY_TEST_EXPECTED_IMAGE:-$DEPLOY_TEST_NEW_IMAGE}" "$DEPLOY_TEST_NEW_IMAGE" validation \
	>"$fixture/validation-output" 2>&1
validation_status=$?
set -e
[[ "$validation_status" -ne 0 ]]
grep -F 'staged Compose file must declare renderd' "$fixture/validation-output" >/dev/null
[[ ! -e "$DEPLOY_TEST_FIXTURE/build-ran" && ! -e "$DEPLOY_TEST_FIXTURE/rollback-started" ]]
unset DEPLOY_TEST_MISSING_RENDERD

# A missing active Compose file makes any existing project container
# untrustworthy: there is no configuration or image state to capture for
# rollback, so the first-deployment path must refuse it before quiescing.
orphan_root="$fixture/orphan-root"
orphan_stage="$orphan_root/.deploy-staging/orphan"
orphan_rollback="$orphan_root/.deploy-rollback/orphan"
mkdir -p "$orphan_root/data" "$orphan_stage/src" "$orphan_stage/diagnostics" "$orphan_rollback"
printf 'SECRET=not-printed\nBOT_DB_PATH=/data/life-ustc-bot.db\n' >"$orphan_stage/.env"
printf 'services:\n  bot:\n    image: new\n  renderd:\n    image: new-renderd\n' >"$orphan_stage/compose.yaml"
export DEPLOY_TEST_FIXTURE="$fixture/orphan"
mkdir -p "$DEPLOY_TEST_FIXTURE"
: >"$orphan_stage/images.tar"
set +e
bash "$remote_script" "$orphan_root" "$orphan_stage" "$orphan_rollback" \
	0123456789012345678901234567890123456789 bot 1 life-ustc-bot life-ustc-renderd "${DEPLOY_TEST_EXPECTED_IMAGE:-$DEPLOY_TEST_NEW_IMAGE}" "$DEPLOY_TEST_NEW_IMAGE" orphan \
	>"$fixture/orphan-output" 2>&1
orphan_status=$?
set -e
[[ "$orphan_status" -ne 0 ]]
grep -F 'active Compose file is missing but project containers already exist' "$fixture/orphan-output" >/dev/null
[[ ! -e "$DEPLOY_TEST_FIXTURE/build-ran" && ! -e "$DEPLOY_TEST_FIXTURE/rollback-started" ]]
! grep -F 'not-printed' "$fixture/orphan-output" >/dev/null

# A failure while preserving the old image happens before the service or
# database is touched. It must report that fact instead of claiming an
# incomplete rollback.
preflight_root="$fixture/preflight-root"
preflight_stage="$preflight_root/.deploy-staging/test"
preflight_rollback="$preflight_root/.deploy-rollback/test"
mkdir -p "$preflight_root/data" "$preflight_stage/src" "$preflight_stage/diagnostics" "$preflight_rollback"
printf 'SECRET=not-printed\nBOT_DB_PATH=/data/life-ustc-bot.db\n' >"$preflight_root/.env"
cp -- "$preflight_root/.env" "$preflight_stage/.env"
printf 'services:\n  bot:\n    image: old\n' >"$preflight_root/compose.yaml"
printf 'services:\n  bot:\n    image: new\n  renderd:\n    image: new-renderd\n' >"$preflight_stage/compose.yaml"
: >"$preflight_stage/images.tar"
set +e
DEPLOY_TEST_FIXTURE="$fixture/preflight" DEPLOY_TEST_FAIL_TAG=1 DEPLOY_TEST_PHASE=running \
	bash "$remote_script" "$preflight_root" "$preflight_stage" "$preflight_rollback" \
		0123456789012345678901234567890123456789 bot 1 life-ustc-bot life-ustc-renderd "${DEPLOY_TEST_EXPECTED_IMAGE:-$DEPLOY_TEST_NEW_IMAGE}" "$DEPLOY_TEST_NEW_IMAGE" preflight \
	>"$fixture/preflight-output" 2>&1
preflight_status=$?
set -e
[[ "$preflight_status" -ne 0 ]]
grep -F 'running service and database were not changed' "$fixture/preflight-output" >/dev/null
! grep -F 'automatic rollback was incomplete' "$fixture/preflight-output" >/dev/null
! grep -F 'not-printed' "$fixture/preflight-output" >/dev/null

# Loading failure or an unexpected image ID must preserve the old image and
# fail before any stop/migration, including a same-revision redeployment.
for failure in load mismatch; do
	failure_root="$fixture/$failure-root"
	failure_stage="$failure_root/.deploy-staging/$failure"
	failure_rollback="$failure_root/.deploy-rollback/$failure"
	mkdir -p "$failure_root/data" "$failure_stage/src" "$failure_stage/diagnostics" "$failure_rollback"
	cp "$preflight_root/.env" "$failure_root/.env"
	cp "$preflight_root/.env" "$failure_stage/.env"
	cp "$preflight_root/compose.yaml" "$failure_root/compose.yaml"
	cp "$preflight_stage/compose.yaml" "$failure_stage/compose.yaml"
	export DEPLOY_TEST_FIXTURE="$fixture/$failure-events"
	mkdir -p "$DEPLOY_TEST_FIXTURE"
	if [[ "$failure" == load ]]; then
		export DEPLOY_TEST_FAIL_LOAD=1
	else
		export DEPLOY_TEST_EXPECTED_IMAGE="sha256:$(printf '9%.0s' {1..64})"
	fi
	: >"$failure_stage/images.tar"
	set +e
	bash "$remote_script" "$failure_root" "$failure_stage" "$failure_rollback" \
		0123456789012345678901234567890123456789 bot 1 life-ustc-bot life-ustc-renderd "${DEPLOY_TEST_EXPECTED_IMAGE:-$DEPLOY_TEST_NEW_IMAGE}" "$DEPLOY_TEST_NEW_IMAGE" "$failure" \
		>"$fixture/$failure-output" 2>&1
	failure_rc=$?
	set -e
	[[ "$failure_rc" -ne 0 ]]
	[[ -f "$DEPLOY_TEST_FIXTURE/rollback-image-tagged" && -f "$DEPLOY_TEST_FIXTURE/images-loaded" ]]
	[[ ! -e "$DEPLOY_TEST_FIXTURE/service-stopped" && ! -e "$DEPLOY_TEST_FIXTURE/migration-ran" ]]
	grep -F 'running service and database were not changed' "$fixture/$failure-output" >/dev/null
	if [[ "$failure" == mismatch ]]; then
		grep -F 'transferred bot image id does not match local build' "$fixture/$failure-output" >/dev/null
	fi
	unset DEPLOY_TEST_FAIL_LOAD DEPLOY_TEST_EXPECTED_IMAGE
done

# A post-promotion failure must stop both newly-created services and restore
# the exact bot/renderd images that were running before the transaction.
sidecar_root="$fixture/sidecar-root"
sidecar_stage="$sidecar_root/.deploy-staging/sidecar"
sidecar_rollback="$sidecar_root/.deploy-rollback/sidecar"
mkdir -p "$sidecar_root/data" "$sidecar_stage/src" "$sidecar_stage/diagnostics" "$sidecar_rollback"
printf 'old source\n' >"$sidecar_root/src.marker"
mkdir -p "$sidecar_root/src"
printf 'old sidecar source\n' >"$sidecar_root/src/version"
printf 'new sidecar source\n' >"$sidecar_stage/src/version"
printf 'SECRET=not-printed\nBOT_DB_PATH=/data/life-ustc-bot.db\n' >"$sidecar_root/.env"
cp -- "$sidecar_root/.env" "$sidecar_stage/.env"
printf 'services:\n  bot:\n    image: old-bot\n  renderd:\n    image: old-renderd\n' >"$sidecar_root/compose.yaml"
cp -- "$sidecar_root/compose.yaml" "$sidecar_stage/compose.yaml"
printf '1111111111111111111111111111111111111111\n' >"$sidecar_root/.deploy-revision"
python3 - "$sidecar_root/data/life-ustc-bot.db" <<'PY'
import sqlite3
import sys

connection = sqlite3.connect(sys.argv[1])
try:
    connection.execute("CREATE TABLE marker (value TEXT NOT NULL)")
    connection.execute("INSERT INTO marker(value) VALUES ('old database')")
    connection.execute("PRAGMA user_version=2")
    connection.commit()
finally:
    connection.close()
PY

sidecar_old_bot_id=aaaaaaaaaaaaaaaa
sidecar_old_renderd_id=bbbbbbbbbbbbbbbb
sidecar_new_bot_id=cccccccccccccccc
sidecar_new_renderd_id=dddddddddddddddd
sidecar_old_bot_image=sha256:$(printf '3%.0s' {1..64})
sidecar_old_renderd_image=sha256:$(printf '4%.0s' {1..64})
sidecar_new_bot_image=sha256:$(printf '5%.0s' {1..64})
sidecar_new_renderd_image=sha256:$(printf '6%.0s' {1..64})
export DEPLOY_TEST_FIXTURE="$fixture/sidecar"
mkdir -p "$DEPLOY_TEST_FIXTURE"
export DEPLOY_TEST_PHASE=running
export DEPLOY_TEST_HAS_RENDERD=1
export DEPLOY_TEST_OLD_BOT_ID="$sidecar_old_bot_id"
export DEPLOY_TEST_OLD_RENDERD_ID="$sidecar_old_renderd_id"
export DEPLOY_TEST_NEW_BOT_ID="$sidecar_new_bot_id"
export DEPLOY_TEST_NEW_RENDERD_ID="$sidecar_new_renderd_id"
export DEPLOY_TEST_OLD_BOT_IMAGE="$sidecar_old_bot_image"
export DEPLOY_TEST_OLD_RENDERD_IMAGE="$sidecar_old_renderd_image"
export DEPLOY_TEST_NEW_BOT_IMAGE="$sidecar_new_bot_image"
export DEPLOY_TEST_NEW_RENDERD_IMAGE="$sidecar_new_renderd_image"
export DEPLOY_TEST_OLD_BOT_REFERENCE=registry.example/life-ustc-bot:previous
export DEPLOY_TEST_OLD_RENDERD_REFERENCE=registry.example/life-ustc-renderd:previous
export DEPLOY_TEST_FAIL_ACTIVE_START=1
touch "$DEPLOY_TEST_FIXTURE/old-bot-running" "$DEPLOY_TEST_FIXTURE/old-renderd-running"

docker() {
	local command="${1:-}"
	local arguments="$*"
	local target
	local format
	local running_file
	case "$command" in
		compose)
			if [[ "$arguments" == *" config --services"* ]]; then
				printf 'bot\nrenderd\n'
				return 0
			fi
			if [[ "$arguments" == *" config "* ]]; then
				return 0
			fi
			if [[ "$arguments" == *" build" ]]; then
				: >"$DEPLOY_TEST_FIXTURE/build-ran"
				return 43
			fi
			if [[ "$arguments" == *" stop "* ]]; then
				: >"$DEPLOY_TEST_FIXTURE/old-bot-stopped"
				: >"$DEPLOY_TEST_FIXTURE/old-renderd-stopped"
				rm -f "$DEPLOY_TEST_FIXTURE/old-bot-running" "$DEPLOY_TEST_FIXTURE/old-renderd-running"
				return 0
			fi
			if [[ "$arguments" == *" logs "* ]]; then
				return 0
			fi
			if [[ "${DEPLOY_TEST_FAIL_ACTIVE_CLEANUP:-0}" == 1 &&
				"$arguments" == *" ps -aq "* &&
				"$arguments" == *".deploy-staging/"* &&
				-n "${DEPLOY_TEST_PROMOTION_ROOT:-}" &&
				! -f "$DEPLOY_TEST_PROMOTION_ROOT/compose.yaml" ]]; then
				return 46
			fi
			if [[ "$arguments" == *" ps -aq bot"* || "$arguments" == *" ps -q bot"* ]]; then
				if [[ "$arguments" == *".deploy-staging/"* ]]; then
					printf '%s\n' "$DEPLOY_TEST_NEW_BOT_ID"
				elif [[ "$arguments" == *".deploy-rollback/"* ]]; then
					printf '%s\n' "$DEPLOY_TEST_OLD_BOT_ID"
				elif [[ -f "$DEPLOY_TEST_FIXTURE/active-started" ]]; then
					printf '%s\n' "$DEPLOY_TEST_NEW_BOT_ID"
				else
					printf '%s\n' "$DEPLOY_TEST_OLD_BOT_ID"
				fi
				return 0
			fi
			if [[ "$arguments" == *" ps -aq renderd"* || "$arguments" == *" ps -q renderd"* ]]; then
				if [[ "$arguments" == *".deploy-staging/"* ]]; then
					printf '%s\n' "$DEPLOY_TEST_NEW_RENDERD_ID"
				elif [[ "$arguments" == *".deploy-rollback/"* ]]; then
					printf '%s\n' "$DEPLOY_TEST_OLD_RENDERD_ID"
				elif [[ -f "$DEPLOY_TEST_FIXTURE/active-started" ]]; then
					printf '%s\n' "$DEPLOY_TEST_NEW_RENDERD_ID"
				else
					printf '%s\n' "$DEPLOY_TEST_OLD_RENDERD_ID"
				fi
				return 0
			fi
			if [[ "$arguments" == *" up "* ]]; then
				if [[ "$arguments" == *".deploy-rollback/"* ]]; then
					printf '%s\n' "$arguments" >>"$DEPLOY_TEST_FIXTURE/up-commands"
					: >"$DEPLOY_TEST_FIXTURE/rollback-started"
					if [[ "$arguments" == *" --no-deps renderd"* ]]; then
						: >"$DEPLOY_TEST_FIXTURE/rollback-renderd-started"
						: >"$DEPLOY_TEST_FIXTURE/old-renderd-running"
					elif [[ "$arguments" == *" --no-deps bot"* ]]; then
						: >"$DEPLOY_TEST_FIXTURE/rollback-bot-started"
						: >"$DEPLOY_TEST_FIXTURE/old-bot-running"
					else
						: >"$DEPLOY_TEST_FIXTURE/rollback-renderd-started"
						: >"$DEPLOY_TEST_FIXTURE/old-bot-running"
						: >"$DEPLOY_TEST_FIXTURE/old-renderd-running"
					fi
					return 0
				fi
				: >"$DEPLOY_TEST_FIXTURE/active-started"
				if [[ "$arguments" == *" --no-deps renderd"* ]]; then
					: >"$DEPLOY_TEST_FIXTURE/new-renderd-running"
					if [[ "${DEPLOY_TEST_FAIL_RENDERD_START:-0}" == 1 ]]; then
						return 42
					fi
				else
					: >"$DEPLOY_TEST_FIXTURE/new-bot-running"
					if [[ "${DEPLOY_TEST_FAIL_ACTIVE_START:-0}" == 1 ]]; then
						return 42
					fi
				fi
				return 0
			fi
			return 0
		;;
		image)
			if [[ "${2:-}" != inspect ]]; then
				return 0
			fi
			if [[ "${3:-}" == --format ]]; then
				target="${5:-}"
				case "$target" in
					custom-bot:rollback-*) printf '%s\n' "$DEPLOY_TEST_OLD_BOT_IMAGE" ;;
					custom-renderd:rollback-*) printf '%s\n' "$DEPLOY_TEST_OLD_RENDERD_IMAGE" ;;
					custom-bot:1111111111111111111111111111111111111111) printf '%s\n' "$DEPLOY_TEST_NEW_BOT_IMAGE" ;;
					custom-renderd:1111111111111111111111111111111111111111) printf '%s\n' "$DEPLOY_TEST_NEW_RENDERD_IMAGE" ;;
					*) printf '%s\n' "$DEPLOY_TEST_NEW_BOT_IMAGE" ;;
				esac
			else
				target="${3:-}"
				case "$target" in
					"$DEPLOY_TEST_OLD_BOT_IMAGE"|"$DEPLOY_TEST_OLD_RENDERD_IMAGE"|"$DEPLOY_TEST_NEW_BOT_IMAGE"|"$DEPLOY_TEST_NEW_RENDERD_IMAGE") : ;;
					*) return 1 ;;
				esac
			fi
			return 0
		;;
		inspect)
			format="${3:-}"
			target="${4:-}"
			case "$format" in
				*State.Running*)
					running_file=""
					case "$target" in
						"$DEPLOY_TEST_OLD_BOT_ID") running_file="$DEPLOY_TEST_FIXTURE/old-bot-running" ;;
						"$DEPLOY_TEST_OLD_RENDERD_ID") running_file="$DEPLOY_TEST_FIXTURE/old-renderd-running" ;;
						"$DEPLOY_TEST_NEW_BOT_ID") running_file="$DEPLOY_TEST_FIXTURE/new-bot-running" ;;
						"$DEPLOY_TEST_NEW_RENDERD_ID") running_file="$DEPLOY_TEST_FIXTURE/new-renderd-running" ;;
					esac
					[[ -n "$running_file" && -f "$running_file" ]] && printf 'true\n' || printf 'false\n'
					;;
				*State.Health*)
					if [[ "${DEPLOY_TEST_HANG_HEALTH:-0}" == 1 && ! -f "$DEPLOY_TEST_FIXTURE/rollback-started" ]]; then
						printf 'starting\n'
					else
						printf 'healthy\n'
					fi
					;;
				*State.Status*) printf 'running\n' ;;
				*.Config.Image*)
					case "$target" in
						"$DEPLOY_TEST_OLD_BOT_ID") printf '%s\n' "$DEPLOY_TEST_OLD_BOT_REFERENCE" ;;
						"$DEPLOY_TEST_OLD_RENDERD_ID") printf '%s\n' "$DEPLOY_TEST_OLD_RENDERD_REFERENCE" ;;
						*) printf 'custom-new\n' ;;
					 esac
					;;
				*.Config.Env*)
					if [[ "${DEPLOY_TEST_EXPECT_NEW_ENV:-0}" == 1 && "$target" == "$DEPLOY_TEST_NEW_BOT_ID" ]]; then
						printf '1111111111111111111111111111111111111111\n'
					else
						printf 'old\n'
					fi
					;;
				*.Image*)
					case "$target" in
						"$DEPLOY_TEST_OLD_BOT_ID") printf '%s\n' "$DEPLOY_TEST_OLD_BOT_IMAGE" ;;
						"$DEPLOY_TEST_OLD_RENDERD_ID") printf '%s\n' "$DEPLOY_TEST_OLD_RENDERD_IMAGE" ;;
						"$DEPLOY_TEST_NEW_BOT_ID") printf '%s\n' "$DEPLOY_TEST_NEW_BOT_IMAGE" ;;
						"$DEPLOY_TEST_NEW_RENDERD_ID") printf '%s\n' "$DEPLOY_TEST_NEW_RENDERD_IMAGE" ;;
						*) printf '\n' ;;
					 esac
					;;
				*) printf '\n' ;;
			esac
			return 0
		;;
		load)
			[[ -f "$DEPLOY_TEST_FIXTURE/bot-image-tagged" ]] || return 43
			[[ -f "$DEPLOY_TEST_FIXTURE/renderd-image-tagged" ]] || return 43
			: >"$DEPLOY_TEST_FIXTURE/images-loaded"
			return 0
		;;
		tag)
			[[ ! -f "$DEPLOY_TEST_FIXTURE/images-loaded" ]] || return 43
			case "${3:-}" in
				custom-bot:rollback-*) [[ "${2:-}" == "$DEPLOY_TEST_OLD_BOT_IMAGE" ]] && : >"$DEPLOY_TEST_FIXTURE/bot-image-tagged" ;;
				custom-renderd:rollback-*) [[ "${2:-}" == "$DEPLOY_TEST_OLD_RENDERD_IMAGE" ]] && : >"$DEPLOY_TEST_FIXTURE/renderd-image-tagged" ;;
				*) return 44 ;;
			esac
			return 0
		;;
		stop)
			case "${2:-}" in
			"$DEPLOY_TEST_NEW_BOT_ID") : >"$DEPLOY_TEST_FIXTURE/new-bot-stopped"; rm -f "$DEPLOY_TEST_FIXTURE/new-bot-running" ;;
			"$DEPLOY_TEST_NEW_RENDERD_ID") : >"$DEPLOY_TEST_FIXTURE/new-renderd-stopped"; rm -f "$DEPLOY_TEST_FIXTURE/new-renderd-running" ;;
			"$DEPLOY_TEST_OLD_BOT_ID") rm -f "$DEPLOY_TEST_FIXTURE/old-bot-running" ;;
			"$DEPLOY_TEST_OLD_RENDERD_ID") rm -f "$DEPLOY_TEST_FIXTURE/old-renderd-running" ;;
			esac
			return 0
		;;
		rm)
			if [[ "${DEPLOY_TEST_FAIL_NEW_REMOVE:-0}" == 1 ]]; then
				case "${3:-${2:-}}" in
				"$DEPLOY_TEST_NEW_BOT_ID"|"$DEPLOY_TEST_NEW_RENDERD_ID") return 45 ;;
				esac
			fi
			case "${3:-${2:-}}" in
			"$DEPLOY_TEST_NEW_BOT_ID") : >"$DEPLOY_TEST_FIXTURE/new-bot-removed"; rm -f "$DEPLOY_TEST_FIXTURE/new-bot-running" ;;
			"$DEPLOY_TEST_NEW_RENDERD_ID") : >"$DEPLOY_TEST_FIXTURE/new-renderd-removed"; rm -f "$DEPLOY_TEST_FIXTURE/new-renderd-running" ;;
			esac
			return 0
		;;
		run)
			: >"$DEPLOY_TEST_FIXTURE/migration-ran"
			return 0
		;;
	esac
	return 0
}
chown() { return 0; }
export -f docker chown

# Interrupt after the old Compose file has moved into rollback but before the
# new file is promoted. No replacement container can exist yet; rollback must
# restore files and restart the captured services without attempting active
# container cleanup against the temporarily missing Compose file.
promotion_root="$fixture/promotion-root"
cp -a "$sidecar_root" "$promotion_root"
promotion_stage="$promotion_root/.deploy-staging/sidecar"
promotion_rollback="$promotion_root/.deploy-rollback/sidecar"
export DEPLOY_TEST_FIXTURE="$fixture/promotion"
mkdir -p "$DEPLOY_TEST_FIXTURE"
export DEPLOY_TEST_PROMOTION_ROOT="$promotion_root"
export DEPLOY_TEST_FAIL_ACTIVE_CLEANUP=1
mv() {
	if [[ "${2:-}" == "$DEPLOY_TEST_PROMOTION_ROOT/compose.yaml" && ! -f "$DEPLOY_TEST_FIXTURE/promotion-mv-started" ]]; then
		: >"$DEPLOY_TEST_FIXTURE/promotion-mv-started"
		sleep 30
	fi
	command mv "$@"
}
export -f mv
touch "$DEPLOY_TEST_FIXTURE/old-bot-running" "$DEPLOY_TEST_FIXTURE/old-renderd-running"
: >"$promotion_stage/images.tar"
set +e
bash "$remote_script" "$promotion_root" "$promotion_stage" "$promotion_rollback" \
	1111111111111111111111111111111111111111 bot 1 custom-bot custom-renderd "$DEPLOY_TEST_NEW_BOT_IMAGE" "$DEPLOY_TEST_NEW_RENDERD_IMAGE" promotion \
	>"$fixture/promotion-output" 2>&1 &
promotion_pid=$!
set -e
for _ in {1..200}; do
	[[ -f "$DEPLOY_TEST_FIXTURE/promotion-mv-started" ]] && break
	sleep 0.05
done
[[ -f "$DEPLOY_TEST_FIXTURE/promotion-mv-started" ]]
kill -TERM "$promotion_pid"
set +e
wait "$promotion_pid"
promotion_status=$?
set -e
[[ "$promotion_status" -ne 0 ]]
grep -F 'previous service and database were restored' "$fixture/promotion-output" >/dev/null
! grep -F 'automatic rollback was incomplete' "$fixture/promotion-output" >/dev/null
grep -F 'old sidecar source' "$promotion_root/src/version" >/dev/null
grep -F 'old-bot' "$promotion_root/compose.yaml" >/dev/null
[[ -f "$DEPLOY_TEST_FIXTURE/rollback-started" ]]
unset -f mv
unset DEPLOY_TEST_PROMOTION_ROOT DEPLOY_TEST_FAIL_ACTIVE_CLEANUP

# If replacement-container cleanup cannot remove a stopped container, rollback
# must be reported as incomplete and must not restart the old services while an
# unresolved container may still belong to the active Compose project.
incomplete_root="$fixture/incomplete-root"
cp -a "$sidecar_root" "$incomplete_root"
incomplete_stage="$incomplete_root/.deploy-staging/sidecar"
incomplete_rollback="$incomplete_root/.deploy-rollback/sidecar"
export DEPLOY_TEST_FIXTURE="$fixture/incomplete"
mkdir -p "$DEPLOY_TEST_FIXTURE"
touch "$DEPLOY_TEST_FIXTURE/old-bot-running" "$DEPLOY_TEST_FIXTURE/old-renderd-running"
export DEPLOY_TEST_FAIL_NEW_REMOVE=1
: >"$incomplete_stage/images.tar"
set +e
bash "$remote_script" "$incomplete_root" "$incomplete_stage" "$incomplete_rollback" \
	1111111111111111111111111111111111111111 bot 1 custom-bot custom-renderd "$DEPLOY_TEST_NEW_BOT_IMAGE" "$DEPLOY_TEST_NEW_RENDERD_IMAGE" incomplete \
	>"$fixture/incomplete-output" 2>&1
incomplete_status=$?
set -e
[[ "$incomplete_status" -ne 0 ]]
grep -F 'automatic rollback was incomplete' "$fixture/incomplete-output" >/dev/null
[[ ! -e "$DEPLOY_TEST_FIXTURE/rollback-started" ]]
[[ ! -e "$DEPLOY_TEST_FIXTURE/old-bot-running" && ! -e "$DEPLOY_TEST_FIXTURE/old-renderd-running" ]]
unset DEPLOY_TEST_FAIL_NEW_REMOVE

export DEPLOY_TEST_FIXTURE="$fixture/sidecar"
: >"$sidecar_stage/images.tar"
set +e
bash "$remote_script" "$sidecar_root" "$sidecar_stage" "$sidecar_rollback" \
	1111111111111111111111111111111111111111 bot 1 custom-bot custom-renderd "$DEPLOY_TEST_NEW_BOT_IMAGE" "$DEPLOY_TEST_NEW_RENDERD_IMAGE" sidecar \
	>"$fixture/sidecar-output" 2>&1
sidecar_status=$?
set -e
[[ "$sidecar_status" -ne 0 ]]
[[ -f "$fixture/sidecar/active-started" ]]
[[ -f "$fixture/sidecar/new-bot-stopped" ]]
[[ -f "$fixture/sidecar/new-renderd-stopped" ]]
[[ -f "$fixture/sidecar/rollback-started" ]]
[[ -f "$fixture/sidecar/rollback-renderd-started" ]]
[[ -f "$fixture/sidecar/bot-image-tagged" ]]
[[ -f "$fixture/sidecar/renderd-image-tagged" ]]
grep -F 'custom-bot:rollback-sidecar' "$sidecar_rollback/image.yaml" >/dev/null
grep -F 'custom-renderd:rollback-sidecar' "$sidecar_rollback/image.yaml" >/dev/null
grep -F 'old sidecar source' "$sidecar_root/src/version" >/dev/null
grep -F 'previous service and database were restored' "$fixture/sidecar-output" >/dev/null

# A stopped sidecar must still be captured for rollback. It is recreated to
# verify the immutable image and then stopped again, while the running bot is
# restored independently.
stopped_root="$fixture/stopped-root"
stopped_stage="$stopped_root/.deploy-staging/stopped"
stopped_rollback="$stopped_root/.deploy-rollback/stopped"
mkdir -p "$stopped_root/data" "$stopped_stage/src" "$stopped_stage/diagnostics" "$stopped_rollback"
mkdir -p "$stopped_root/src"
printf 'old stopped-dependency source\n' >"$stopped_root/src/version"
printf 'new stopped-dependency source\n' >"$stopped_stage/src/version"
printf 'SECRET=not-printed\nBOT_DB_PATH=/data/life-ustc-bot.db\n' >"$stopped_root/.env"
cp -- "$stopped_root/.env" "$stopped_stage/.env"
printf 'services:\n  bot:\n    image: old-bot\n  renderd:\n    image: old-renderd\n' >"$stopped_root/compose.yaml"
cp -- "$stopped_root/compose.yaml" "$stopped_stage/compose.yaml"
printf '1111111111111111111111111111111111111111\n' >"$stopped_root/.deploy-revision"
python3 - "$stopped_root/data/life-ustc-bot.db" <<'PY'
import sqlite3
import sys

connection = sqlite3.connect(sys.argv[1])
try:
    connection.execute("CREATE TABLE marker (value TEXT NOT NULL)")
    connection.execute("INSERT INTO marker(value) VALUES ('old database')")
    connection.execute("PRAGMA user_version=2")
    connection.commit()
finally:
    connection.close()
PY

export DEPLOY_TEST_FIXTURE="$fixture/stopped"
mkdir -p "$DEPLOY_TEST_FIXTURE"
export DEPLOY_TEST_FAIL_ACTIVE_START=1
touch "$DEPLOY_TEST_FIXTURE/old-bot-running"
rm -f "$DEPLOY_TEST_FIXTURE/old-renderd-running"
: >"$stopped_stage/images.tar"
set +e
bash "$remote_script" "$stopped_root" "$stopped_stage" "$stopped_rollback" \
	1111111111111111111111111111111111111111 bot 1 custom-bot custom-renderd "$DEPLOY_TEST_NEW_BOT_IMAGE" "$DEPLOY_TEST_NEW_RENDERD_IMAGE" stopped \
	>"$fixture/stopped-output" 2>&1
stopped_status=$?
set -e
[[ "$stopped_status" -ne 0 ]]
[[ -f "$fixture/stopped/active-started" ]]
[[ -f "$fixture/stopped/new-bot-stopped" ]]
[[ -f "$fixture/stopped/new-renderd-stopped" ]]
[[ -f "$fixture/stopped/rollback-started" ]]
[[ -f "$fixture/stopped/rollback-bot-started" ]]
[[ -f "$fixture/stopped/rollback-renderd-started" ]]
[[ -f "$fixture/stopped/old-bot-running" ]]
[[ ! -f "$fixture/stopped/old-renderd-running" ]]
grep -F -- '--no-deps bot' "$fixture/stopped/up-commands" >/dev/null
grep -F -- '--no-deps renderd' "$fixture/stopped/up-commands" >/dev/null
grep -F 'renderd_running=0' "$stopped_rollback/previous-state" >/dev/null
grep -F 'previous service and database were restored' "$fixture/stopped-output" >/dev/null

# A termination signal during the health wait must enter the same transaction
# rollback path as an ordinary failure. The old state marker is kept intact,
# both replacement containers are removed, and the captured services return.
signal_root="$fixture/signal-root"
signal_stage="$signal_root/.deploy-staging/signal"
signal_rollback="$signal_root/.deploy-rollback/signal"
mkdir -p "$signal_root/data" "$signal_stage/src" "$signal_stage/diagnostics" "$signal_rollback" "$signal_root/src"
printf 'old signal source\n' >"$signal_root/src/version"
printf 'new signal source\n' >"$signal_stage/src/version"
printf 'SECRET=not-printed\nBOT_DB_PATH=/data/life-ustc-bot.db\n' >"$signal_root/.env"
cp -- "$signal_root/.env" "$signal_stage/.env"
printf 'services:\n  bot:\n    image: old-bot\n  renderd:\n    image: old-renderd\n' >"$signal_root/compose.yaml"
cp -- "$signal_root/compose.yaml" "$signal_stage/compose.yaml"
printf '1111111111111111111111111111111111111111\n' >"$signal_root/.deploy-revision"
printf 'revision=old-state\n' >"$signal_root/.deploy-state"
python3 - "$signal_root/data/life-ustc-bot.db" <<'PY'
import sqlite3
import sys

connection = sqlite3.connect(sys.argv[1])
try:
    connection.execute("CREATE TABLE marker (value TEXT NOT NULL)")
    connection.execute("INSERT INTO marker(value) VALUES ('old database')")
    connection.execute("PRAGMA user_version=2")
    connection.commit()
finally:
    connection.close()
PY

export DEPLOY_TEST_FIXTURE="$fixture/signal"
mkdir -p "$DEPLOY_TEST_FIXTURE"
unset DEPLOY_TEST_FAIL_ACTIVE_START DEPLOY_TEST_FAIL_RENDERD_START
export DEPLOY_TEST_HANG_HEALTH=1
touch "$DEPLOY_TEST_FIXTURE/old-bot-running" "$DEPLOY_TEST_FIXTURE/old-renderd-running"
: >"$signal_stage/images.tar"
set +e
bash "$remote_script" "$signal_root" "$signal_stage" "$signal_rollback" \
	1111111111111111111111111111111111111111 bot 1 custom-bot custom-renderd "$DEPLOY_TEST_NEW_BOT_IMAGE" "$DEPLOY_TEST_NEW_RENDERD_IMAGE" signal \
	>"$fixture/signal-output" 2>&1 &
signal_pid=$!
set -e
for _ in {1..100}; do
	[[ -f "$DEPLOY_TEST_FIXTURE/active-started" ]] && break
	sleep 0.05
done
[[ -f "$DEPLOY_TEST_FIXTURE/active-started" ]]
kill -TERM "$signal_pid"
set +e
wait "$signal_pid"
signal_status=$?
set -e
[[ "$signal_status" -ne 0 ]]
[[ -f "$DEPLOY_TEST_FIXTURE/new-bot-removed" ]]
[[ -f "$DEPLOY_TEST_FIXTURE/new-renderd-removed" ]]
[[ -f "$DEPLOY_TEST_FIXTURE/rollback-started" ]]
grep -F 'old signal source' "$signal_root/src/version" >/dev/null
grep -F 'revision=old-state' "$signal_root/.deploy-state" >/dev/null
grep -F 'old database' <(python3 - "$signal_root/data/life-ustc-bot.db" <<'PY'
import sqlite3
import sys

connection = sqlite3.connect(sys.argv[1])
try:
    print(connection.execute("SELECT value FROM marker").fetchone()[0])
finally:
    connection.close()
PY
) >/dev/null
[[ ! -e "$signal_root/.deploy-revision.tmp" && ! -e "$signal_root/.deploy-state.tmp" ]]
unset DEPLOY_TEST_HANG_HEALTH

# Interrupt while the new deployment state is being atomically renamed. The
# temporary marker must be quarantined and the previous state restored.
state_root="$fixture/state-root"
state_stage="$state_root/.deploy-staging/state"
state_rollback="$state_root/.deploy-rollback/state"
mkdir -p "$state_root/data" "$state_stage/src" "$state_stage/diagnostics" "$state_rollback" "$state_root/src"
printf 'old state source\n' >"$state_root/src/version"
printf 'new state source\n' >"$state_stage/src/version"
printf 'SECRET=not-printed\nBOT_DB_PATH=/data/life-ustc-bot.db\n' >"$state_root/.env"
cp -- "$state_root/.env" "$state_stage/.env"
printf 'services:\n  bot:\n    image: old-bot\n  renderd:\n    image: old-renderd\n' >"$state_root/compose.yaml"
cp -- "$state_root/compose.yaml" "$state_stage/compose.yaml"
printf '1111111111111111111111111111111111111111\n' >"$state_root/.deploy-revision"
printf 'revision=previous-state\n' >"$state_root/.deploy-state"
python3 - "$state_root/data/life-ustc-bot.db" <<'PY'
import sqlite3
import sys

connection = sqlite3.connect(sys.argv[1])
try:
    connection.execute("CREATE TABLE marker (value TEXT NOT NULL)")
    connection.execute("INSERT INTO marker(value) VALUES ('old database')")
    connection.execute("PRAGMA user_version=2")
    connection.commit()
finally:
    connection.close()
PY

export DEPLOY_TEST_FIXTURE="$fixture/state"
mkdir -p "$DEPLOY_TEST_FIXTURE"
unset DEPLOY_TEST_FAIL_ACTIVE_START DEPLOY_TEST_FAIL_RENDERD_START DEPLOY_TEST_HANG_HEALTH
export DEPLOY_TEST_EXPECT_NEW_ENV=1
export DEPLOY_TEST_STATE_ROOT="$state_root"
mv() {
	if [[ "${2:-}" == "$DEPLOY_TEST_STATE_ROOT/.deploy-state.tmp" && ! -f "$DEPLOY_TEST_FIXTURE/state-mv-started" ]]; then
		: >"$DEPLOY_TEST_FIXTURE/state-mv-started"
		sleep 30
	fi
	command mv "$@"
}
export -f mv
touch "$DEPLOY_TEST_FIXTURE/old-bot-running" "$DEPLOY_TEST_FIXTURE/old-renderd-running"
: >"$state_stage/images.tar"
set +e
bash "$remote_script" "$state_root" "$state_stage" "$state_rollback" \
	1111111111111111111111111111111111111111 bot 1 custom-bot custom-renderd "$DEPLOY_TEST_NEW_BOT_IMAGE" "$DEPLOY_TEST_NEW_RENDERD_IMAGE" state \
	>"$fixture/state-output" 2>&1 &
state_pid=$!
set -e
for _ in {1..200}; do
	[[ -f "$DEPLOY_TEST_FIXTURE/state-mv-started" ]] && break
	sleep 0.05
done
if [[ ! -f "$DEPLOY_TEST_FIXTURE/state-mv-started" ]]; then
	sed -n '1,120p' "$fixture/state-output" >&2 || true
	false
fi
kill -TERM "$state_pid"
set +e
wait "$state_pid"
state_status=$?
set -e
[[ "$state_status" -ne 0 ]]
grep -F 'old state source' "$state_root/src/version" >/dev/null
grep -F 'revision=previous-state' "$state_root/.deploy-state" >/dev/null
[[ ! -e "$state_root/.deploy-state.tmp" && ! -e "$state_root/.deploy-revision.tmp" ]]
[[ -f "$state_rollback/previous-state.snapshot" ]]
unset -f mv
unset DEPLOY_TEST_STATE_ROOT DEPLOY_TEST_EXPECT_NEW_ENV

# Local image builds use a fresh git fixture so the archive and ignored secret
# handling are exercised without touching this checkout. The SSH mock records
# every remote command; build and transfer failures must happen before any
# remote deployment command is attempted.
local_root="$fixture/local-root"
mkdir -p "$local_root/scripts" "$local_root/renderd"
cp -- "$root/scripts/deploy-cn.sh" "$local_root/scripts/deploy-cn.sh"
cp -- "$root/compose.yaml" "$local_root/compose.yaml"
cp -- "$root/Dockerfile" "$local_root/Dockerfile"
cp -- "$root/renderd/Dockerfile" "$local_root/renderd/Dockerfile"
printf '.env\n' >"$local_root/.gitignore"
printf 'SECRET=local-secret\nBOT_DB_PATH=/data/life-ustc-bot.db\n' >"$local_root/.env"
git -C "$local_root" init -q
git -C "$local_root" add .gitignore compose.yaml Dockerfile renderd/Dockerfile scripts/deploy-cn.sh
git -C "$local_root" -c user.name=deploy-test -c user.email=deploy-test@example.invalid commit -q -m fixture
production_marker="$fixture/production-marker"
printf 'old production\n' >"$production_marker"
local_events="$fixture/local-events"

docker() {
	local command="${1:-}"
	local arguments="$*"
	local context
	case "$command" in
		buildx)
			printf 'docker %s\n' "$arguments" >>"$DEPLOY_TEST_LOCAL_EVENTS"
			context="${!#}"
			[[ "$arguments" == *'--platform linux/amd64'* ]]
			[[ -f "$context/Dockerfile" ]]
			[[ ! -e "$context/.env" ]]
			if [[ "$arguments" == *'renderd/Dockerfile'* ]]; then
				[[ "$context" == */renderd ]]
			else
				[[ "$context" != */renderd ]]
			fi
			[[ "${DEPLOY_TEST_LOCAL_FAIL_BUILD:-0}" != 1 ]]
			return
		;;
		image)
			[[ "${2:-}" == inspect && "${3:-}" == --format ]]
			if [[ "${5:-}" == *renderd* ]]; then
				printf 'sha256:%s\n' "$(printf '2%.0s' {1..64})"
			else
				printf 'sha256:%s\n' "$(printf '1%.0s' {1..64})"
			fi
			return
		;;
		save)
			printf 'docker %s\n' "$arguments" >>"$DEPLOY_TEST_LOCAL_EVENTS"
			printf 'complete image archive\n'
			return
		;;
	esac
	return 0
}

ssh() {
	local host="$1"
	shift
	local command="$*"
	printf 'ssh %s %s\n' "$host" "$command" >>"$DEPLOY_TEST_LOCAL_EVENTS"
	cat >/dev/null
	if [[ "$command" == *'/images.tar'* ]]; then
		[[ "${DEPLOY_TEST_LOCAL_FAIL_TRANSFER:-0}" != 1 ]]
	fi
}

scp() {
	printf 'scp %s\n' "$*" >>"$DEPLOY_TEST_LOCAL_EVENTS"
}

export DEPLOY_TEST_LOCAL_EVENTS="$local_events"
export -f docker ssh scp

export REMOTE_HOST=deploy@example
export REMOTE_DIR=/srv/life-ustc
export DEPLOY_IMAGE_REPOSITORY=life-ustc-bot
export DEPLOY_RENDERD_IMAGE_REPOSITORY=life-ustc-renderd
export DEPLOY_HEALTH_TIMEOUT=1
export DEPLOY_TEST_LOCAL_FAIL_BUILD=1
set +e
"$local_root/scripts/deploy-cn.sh" >"$fixture/local-build-failure-output" 2>&1
local_build_failure_rc=$?
set -e
[[ "$local_build_failure_rc" -ne 0 ]]
grep -F 'docker buildx build' "$local_events" >/dev/null
! grep -F 'docker load' "$local_events" >/dev/null
! grep -F 'bash -s --' "$local_events" >/dev/null
grep -F 'old production' "$production_marker" >/dev/null
unset DEPLOY_TEST_LOCAL_FAIL_BUILD

: >"$local_events"
export DEPLOY_TEST_LOCAL_FAIL_TRANSFER=1
set +e
"$local_root/scripts/deploy-cn.sh" >"$fixture/local-transfer-failure-output" 2>&1
local_transfer_failure_rc=$?
set -e
[[ "$local_transfer_failure_rc" -ne 0 ]]
grep -F 'docker save' "$local_events" >/dev/null
grep -F '/images.tar' "$local_events" >/dev/null
[[ "$(grep -Fc 'bash -s --' "$local_events")" -eq 1 ]]
grep -F 'old production' "$production_marker" >/dev/null
unset DEPLOY_TEST_LOCAL_FAIL_TRANSFER

: >"$local_events"
"$local_root/scripts/deploy-cn.sh" >"$fixture/local-success-output" 2>&1
[[ "$(grep -Fc 'docker buildx build' "$local_events")" -eq 2 ]]
grep -F 'docker save life-ustc-bot:' "$local_events" >/dev/null
grep -F '/images.tar' "$local_events" >/dev/null
grep -F 'bash -s --' "$local_events" >/dev/null
! grep -F 'compose_stage build' "$local_events" >/dev/null
grep -F 'old production' "$production_marker" >/dev/null
unset -f docker ssh scp
unset DEPLOY_TEST_LOCAL_EVENTS REMOTE_HOST REMOTE_DIR DEPLOY_IMAGE_REPOSITORY DEPLOY_RENDERD_IMAGE_REPOSITORY DEPLOY_HEALTH_TIMEOUT

output="$(
	DEPLOY_DRY_RUN=1 \
	REMOTE_HOST=deploy@example \
	REMOTE_DIR=/srv/life-ustc \
	"$root/scripts/deploy-cn.sh" 2>&1
)"

grep -F 'dry-run would deploy revision ' <<<"$output" >/dev/null
grep -F 'dry-run performs no SSH, SCP, Docker, source, or database changes' <<<"$output" >/dev/null

echo "deploy-cn dry-run test passed"
