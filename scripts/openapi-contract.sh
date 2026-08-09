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
		echo "usage: $0 {verify|commit|update SOURCE SERVER_SHA}" >&2
		exit 2
		;;
esac
