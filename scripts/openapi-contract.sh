#!/bin/sh

set -eu

spec=api/openapi.json
provenance=api/openapi.provenance
repository=Life-USTC/server

read_value() {
	key=$1
	file=$2
	value=$(sed -n "s/^${key}=//p" "$file")
	count=$(grep -c "^${key}=" "$file" || true)
	if [ "$count" -ne 1 ] || [ -z "$value" ]; then
		echo "invalid OpenAPI provenance: expected one ${key} entry" >&2
		exit 1
	fi
	printf '%s\n' "$value"
}

validate_sha() {
	value=$1
	if [ "${#value}" -ne 40 ]; then
		echo "invalid server commit SHA: expected 40 lowercase hexadecimal characters" >&2
		exit 1
	fi
	case $value in
		*[!0-9a-f]*)
			echo "invalid server commit SHA: expected 40 lowercase hexadecimal characters" >&2
			exit 1
			;;
	esac
}

validate_hash() {
	value=$1
	if [ "${#value}" -ne 64 ]; then
		echo "invalid OpenAPI SHA-256: expected 64 lowercase hexadecimal characters" >&2
		exit 1
	fi
	case $value in
		*[!0-9a-f]*)
			echo "invalid OpenAPI SHA-256: expected 64 lowercase hexadecimal characters" >&2
			exit 1
			;;
	esac
}

# Resolves the ref that stands for the released history of $repository inside
# SERVER_DIR. A missing ref has to fail loudly rather than be skipped: a
# reachability check that cannot be evaluated is worse than no check at all.
server_main_ref() {
	dir=$1
	candidates=${OPENAPI_SERVER_MAIN_REF:-"refs/remotes/origin/main refs/heads/main"}
	for candidate in $candidates; do
		if git -C "$dir" rev-parse --verify --quiet "${candidate}^{commit}" >/dev/null 2>&1; then
			printf '%s\n' "$candidate"
			return 0
		fi
	done
	echo "cannot check pin reachability: none of $candidates exist in $dir" >&2
	echo "fetch the release branch first, e.g. git -C $dir fetch --no-tags origin +refs/heads/main:refs/remotes/origin/main" >&2
	exit 1
}

# $repository squash-merges, so every released commit is on main and a
# pull-request head commit never is. A PR head stays fetchable by SHA forever,
# so pinning one produces a client generated from a branch that was thrown
# away, while every other check here still passes. Pinning is only safe when
# the commit is reachable from main.
verify_reachable() {
	server_dir=$1
	pinned_commit=$(read_value commit "$provenance")
	validate_sha "$pinned_commit"

	if [ ! -e "$server_dir/.git" ]; then
		echo "cannot check pin reachability: $server_dir is not a git checkout of $repository" >&2
		exit 1
	fi
	if [ "$(git -C "$server_dir" rev-parse --is-shallow-repository)" != "false" ]; then
		echo "cannot check pin reachability: $server_dir is a shallow clone, so ancestry cannot be evaluated" >&2
		echo "check out $repository with fetch-depth: 0 before verifying the pin" >&2
		exit 1
	fi
	if ! git -C "$server_dir" rev-parse --verify --quiet "${pinned_commit}^{commit}" >/dev/null 2>&1; then
		echo "cannot check pin reachability: pinned commit $pinned_commit is not present in $server_dir" >&2
		exit 1
	fi

	main_ref=$(server_main_ref "$server_dir")
	ancestry=0
	git -C "$server_dir" merge-base --is-ancestor "$pinned_commit" "$main_ref" || ancestry=$?
	if [ "$ancestry" -eq 1 ]; then
		echo "pinned server commit $pinned_commit is NOT reachable from $main_ref of $repository" >&2
		echo "$repository squash-merges, so this pin is a pull-request head or another commit that never landed on main; the generated client would be built from a dead branch" >&2
		echo "re-run: make sync-openapi OPENAPI_SOURCE=$server_dir/public/openapi.generated.json OPENAPI_SERVER_SHA=<a commit on main>" >&2
		exit 1
	elif [ "$ancestry" -ne 0 ]; then
		echo "cannot check pin reachability: git merge-base --is-ancestor $pinned_commit $main_ref failed with status $ancestry" >&2
		exit 1
	fi
}

verify() {
	if [ ! -f "$provenance" ]; then
		echo "missing OpenAPI provenance: $provenance" >&2
		exit 1
	fi
	if [ "$(wc -l < "$provenance" | tr -d ' ')" -ne 3 ]; then
		echo "invalid OpenAPI provenance: expected exactly three entries" >&2
		exit 1
	fi

	pinned_repository=$(read_value repository "$provenance")
	pinned_commit=$(read_value commit "$provenance")
	pinned_hash=$(read_value sha256 "$provenance")
	if [ "$pinned_repository" != "$repository" ]; then
		echo "invalid OpenAPI repository: $pinned_repository" >&2
		exit 1
	fi
	validate_sha "$pinned_commit"
	validate_hash "$pinned_hash"
	actual_hash=$(sha256sum "$spec" | awk '{print $1}')
	if [ "$actual_hash" != "$pinned_hash" ]; then
		echo "OpenAPI provenance hash mismatch: expected $pinned_hash, got $actual_hash" >&2
		exit 1
	fi
}

case ${1:-} in
	verify)
		verify
		;;
	commit)
		verify
		read_value commit "$provenance"
		;;
	verify-reachable)
		server_dir=${2:-}
		if [ -z "$server_dir" ]; then
			echo "usage: $0 verify-reachable SERVER_DIR" >&2
			exit 2
		fi
		verify
		verify_reachable "$server_dir"
		;;
	update)
		source=${2:-}
		server_sha=${3:-}
		if [ ! -f "$source" ]; then
			echo "missing OpenAPI source: $source" >&2
			exit 1
		fi
		validate_sha "$server_sha"
		cp "$source" "$spec"
		hash=$(sha256sum "$spec" | awk '{print $1}')
		printf 'repository=%s\ncommit=%s\nsha256=%s\n' "$repository" "$server_sha" "$hash" > "$provenance"
		verify
		;;
	*)
		echo "usage: $0 {verify|commit|verify-reachable SERVER_DIR|update SOURCE SERVER_SHA}" >&2
		exit 2
		;;
esac
