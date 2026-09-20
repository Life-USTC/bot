#!/usr/bin/env bash

# Exercises scripts/openapi-contract.sh against synthetic Life-USTC/server
# checkouts. The point of the suite is the reachability assertion: a pull
# request head stays fetchable by SHA forever, so the contract can be pinned to
# a commit that never landed on main while every other check still passes --
# the provenance verifies, the server checkout is at the pinned commit, and the
# vendored spec is byte-identical to the source.

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
script="$root/scripts/openapi-contract.sh"
workdir="$(mktemp -d "${TMPDIR:-/tmp}/openapi-contract-test.XXXXXXXX")"
failures=0
test_phase="initialization"

cleanup() {
	rm -rf -- "$workdir"
}
on_error() {
	local status=$?
	trap - ERR
	printf 'openapi-contract smoke test crashed: phase=%s status=%s command=%s\n' \
		"$test_phase" "$status" "$BASH_COMMAND" >&2
	exit "$status"
}
trap on_error ERR
trap cleanup EXIT

pass() {
	printf 'ok   - %s\n' "$1"
}
fail() {
	printf 'FAIL - %s\n' "$1" >&2
	if [[ -n "${2:-}" ]]; then
		printf '       %s\n' "$2" >&2
	fi
	failures=$((failures + 1))
}

git_quiet() {
	git -c user.name="contract test" -c user.email="contract@test.invalid" \
		-c commit.gpgsign=false -c init.defaultBranch=main "$@" >/dev/null 2>&1
}

# Builds a server checkout whose main branch is a squash-merge style line of
# commits, plus one commit on a side branch that is never merged. Echoes
# "<main_tip> <side_head> <first_main>".
make_server() {
	local dir="$1"
	local first main_tip side_head

	mkdir -p "$dir/public"
	git_quiet init "$dir"
	printf '{"openapi":"3.1.0","info":{"title":"first"}}\n' >"$dir/public/openapi.generated.json"
	git_quiet -C "$dir" add -A
	git_quiet -C "$dir" commit -m "feat: first release contract"
	first="$(git -C "$dir" rev-parse HEAD)"

	# A pull request branched off main and was never merged, exactly like
	# fea7bb21 ("fix(young): complete release contracts", 2026-09-15).
	git_quiet -C "$dir" checkout -b pull-request
	printf '{"openapi":"3.1.0","info":{"title":"pull request head"}}\n' >"$dir/public/openapi.generated.json"
	git_quiet -C "$dir" add -A
	git_quiet -C "$dir" commit -m "fix(young): complete release contracts"
	side_head="$(git -C "$dir" rev-parse HEAD)"

	git_quiet -C "$dir" checkout main
	printf '{"openapi":"3.1.0","info":{"title":"second"}}\n' >"$dir/public/openapi.generated.json"
	git_quiet -C "$dir" add -A
	git_quiet -C "$dir" commit -m "feat: squash-merged release contract"
	main_tip="$(git -C "$dir" rev-parse HEAD)"

	printf '%s %s %s\n' "$main_tip" "$side_head" "$first"
}

# Builds a bot checkout pinned to $2 with a vendored spec copied from $3.
make_consumer() {
	local dir="$1" commit="$2" source_spec="$3"
	local hash

	mkdir -p "$dir/api"
	cp "$source_spec" "$dir/api/openapi.json"
	hash="$(sha256sum "$dir/api/openapi.json" | awk '{print $1}')"
	printf 'repository=Life-USTC/server\ncommit=%s\nsha256=%s\n' \
		"$commit" "$hash" >"$dir/api/openapi.provenance"
}

# Runs the script inside a bot checkout and reports status plus combined output.
run_contract() {
	local consumer="$1"
	shift
	local status=0
	output="$(cd "$consumer" && "$script" "$@" 2>&1)" || status=$?
	return "$status"
}

test_phase="shell syntax"
sh -n "$script"
bash -n "${BASH_SOURCE[0]}"

test_phase="fixture construction"
server="$workdir/server"
read -r main_tip side_head first_main < <(make_server "$server")
git_quiet -C "$server" checkout main

test_phase="the exact CI hole: provenance, HEAD, and spec all agree"
# This is what CI asserted before the reachability check existed. Every one of
# these passes for a pull-request head pin, which is why CI went green on a
# contract generated from a dead branch.
consumer="$workdir/consumer-incident"
git_quiet -C "$server" checkout "$side_head"
make_consumer "$consumer" "$side_head" "$server/public/openapi.generated.json"
if run_contract "$consumer" verify; then
	pass "the old checks pass for a pull-request head pin (provenance)"
else
	fail "the old checks pass for a pull-request head pin (provenance)" "$output"
fi
if [[ "$(git -C "$server" rev-parse HEAD)" == "$side_head" ]] \
	&& cmp -s "$server/public/openapi.generated.json" "$consumer/api/openapi.json"; then
	pass "the old checks pass for a pull-request head pin (HEAD and spec)"
else
	fail "the old checks pass for a pull-request head pin (HEAD and spec)" "fixture is wrong"
fi
if run_contract "$consumer" verify-reachable "$server"; then
	fail "verify-reachable rejects the pull-request head pin" "the script accepted $side_head"
elif [[ "$output" != *"is NOT reachable from"* ]]; then
	fail "verify-reachable explains why the pull-request head pin is wrong" "$output"
else
	pass "verify-reachable rejects the pull-request head pin"
fi
git_quiet -C "$server" checkout main

test_phase="commits on main are accepted"
consumer="$workdir/consumer-tip"
make_consumer "$consumer" "$main_tip" "$server/public/openapi.generated.json"
if run_contract "$consumer" verify-reachable "$server"; then
	pass "verify-reachable accepts the main tip"
else
	fail "verify-reachable accepts the main tip" "$output"
fi

consumer="$workdir/consumer-ancestor"
make_consumer "$consumer" "$first_main" "$server/public/openapi.generated.json"
if run_contract "$consumer" verify-reachable "$server"; then
	pass "verify-reachable accepts an older commit on main"
else
	fail "verify-reachable accepts an older commit on main" "$output"
fi

test_phase="refs/remotes/origin/main is honoured"
remote_clone="$workdir/server-clone"
git_quiet clone "file://$server" "$remote_clone"
git_quiet -C "$remote_clone" checkout "$first_main"
consumer="$workdir/consumer-remote-ref"
make_consumer "$consumer" "$first_main" "$server/public/openapi.generated.json"
if run_contract "$consumer" verify-reachable "$remote_clone"; then
	pass "verify-reachable resolves main through refs/remotes/origin/main"
else
	fail "verify-reachable resolves main through refs/remotes/origin/main" "$output"
fi
consumer="$workdir/consumer-remote-ref-bad"
make_consumer "$consumer" "$side_head" "$server/public/openapi.generated.json"
if run_contract "$consumer" verify-reachable "$remote_clone"; then
	fail "verify-reachable still rejects a PR head against refs/remotes/origin/main" "accepted $side_head"
else
	pass "verify-reachable still rejects a PR head against refs/remotes/origin/main"
fi

test_phase="a check that cannot be evaluated fails loudly"
shallow="$workdir/server-shallow"
git_quiet clone --depth 1 "file://$server" "$shallow"
consumer="$workdir/consumer-shallow"
make_consumer "$consumer" "$main_tip" "$server/public/openapi.generated.json"
if run_contract "$consumer" verify-reachable "$shallow"; then
	fail "verify-reachable refuses a shallow server checkout" "the script reported success"
elif [[ "$output" != *"shallow clone"* ]]; then
	fail "verify-reachable names the shallow checkout as the reason" "$output"
else
	pass "verify-reachable refuses a shallow server checkout"
fi

no_main="$workdir/server-no-main"
git_quiet clone "file://$server" "$no_main"
git_quiet -C "$no_main" checkout -b detached-work
git_quiet -C "$no_main" branch -D main
git_quiet -C "$no_main" remote remove origin
consumer="$workdir/consumer-no-main"
make_consumer "$consumer" "$main_tip" "$server/public/openapi.generated.json"
if run_contract "$consumer" verify-reachable "$no_main"; then
	fail "verify-reachable refuses a checkout without a main ref" "the script reported success"
elif [[ "$output" != *"cannot check pin reachability"* ]]; then
	fail "verify-reachable names the missing main ref as the reason" "$output"
else
	pass "verify-reachable refuses a checkout without a main ref"
fi

consumer="$workdir/consumer-absent-commit"
make_consumer "$consumer" "0123456789abcdef0123456789abcdef01234567" "$server/public/openapi.generated.json"
if run_contract "$consumer" verify-reachable "$server"; then
	fail "verify-reachable refuses a pin the server checkout does not contain" "the script reported success"
elif [[ "$output" != *"is not present in"* ]]; then
	fail "verify-reachable names the absent commit as the reason" "$output"
else
	pass "verify-reachable refuses a pin the server checkout does not contain"
fi

test_phase="argument handling"
consumer="$workdir/consumer-tip"
if run_contract "$consumer" verify-reachable; then
	fail "verify-reachable requires SERVER_DIR" "the script reported success"
elif [[ "$output" != *"usage:"* ]]; then
	fail "verify-reachable prints usage without SERVER_DIR" "$output"
else
	pass "verify-reachable requires SERVER_DIR"
fi

test_phase="reporting"
if [[ "$failures" -ne 0 ]]; then
	printf '\n%s openapi-contract check(s) failed\n' "$failures" >&2
	exit 1
fi
printf '\nall openapi-contract checks passed\n'
