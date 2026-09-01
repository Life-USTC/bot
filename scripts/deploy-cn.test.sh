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
printf 'SECRET=not-printed\nBOT_DB_PATH=/data/life-ustc-bot.db\n' >"$test_root/.env"
cp -- "$test_root/.env" "$test_stage/.env"
printf 'services:\n  bot:\n    image: old\n' >"$test_root/compose.yaml"
cp -- "$test_root/compose.yaml" "$test_stage/compose.yaml"
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
export DEPLOY_TEST_NEW_IMAGE=sha256:$(printf '2%.0s' {1..64})
docker() {
	local command="${1:-}"
	if [[ "$command" == compose ]]; then
		local arguments="$*"
		if [[ "$arguments" == *" config "* || "$arguments" == *" build "* ]]; then
			return 0
		fi
		if [[ "$arguments" == *" stop "* ]]; then
			DEPLOY_TEST_PHASE=stopped
			return 0
		fi
		if [[ "$arguments" == *" logs "* ]]; then
			return 0
		fi
		if [[ "$arguments" == *" ps -q "* ]]; then
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
	if [[ "$command" == image && "${2:-}" == inspect ]]; then
		printf '%s\n' "$DEPLOY_TEST_NEW_IMAGE"
		return 0
	fi
	if [[ "$command" == inspect ]]; then
		local format="${3:-}"
		case "$format" in
			*State.Running*) [[ "$DEPLOY_TEST_PHASE" == running ]] && printf 'true\n' || printf 'false\n' ;;
			*State.Health*) printf 'healthy\n' ;;
			*State.Status*) printf 'running\n' ;;
			*'.Image'*) printf '%s\n' "$DEPLOY_TEST_OLD_IMAGE" ;;
			*) printf '\n' ;;
		esac
		return 0
	fi
	if [[ "$command" == tag || "$command" == stop ]]; then
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
set +e
bash "$remote_script" "$test_root" "$test_stage" "$test_rollback" \
	0123456789012345678901234567890123456789 bot 1 life-ustc-bot test \
	>"$fixture/output" 2>&1
failure_status=$?
set -e
[[ "$failure_status" -ne 0 ]]
[[ -f "$fixture/migration-ran" ]]
[[ ! -f "$fixture/active-started" ]]
[[ -f "$fixture/rollback-started" ]]
grep -F 'old source' "$test_root/src/version" >/dev/null
grep -F 'old-revision' "$test_root/.deploy-revision" >/dev/null
grep -F 'SECRET=not-printed' "$test_root/.env" >/dev/null
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

output="$(
	DEPLOY_DRY_RUN=1 \
	REMOTE_HOST=deploy@example \
	REMOTE_DIR=/srv/life-ustc \
	"$root/scripts/deploy-cn.sh" 2>&1
)"

grep -F 'dry-run would deploy revision ' <<<"$output" >/dev/null
grep -F 'dry-run performs no SSH, SCP, Docker, source, or database changes' <<<"$output" >/dev/null

echo "deploy-cn dry-run test passed"
