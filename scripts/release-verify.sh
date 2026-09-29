#!/bin/bash
# Verifies a signed release directory the way install-agent.sh does before
# installing: checksums.txt.sig must be a signature by the key built into
# the released install-agent.sh, and every file must match checksums.txt.
# The release workflow runs it after signing, so a signing key that does
# not match the published key fails the release instead of every install.
#
# Usage: scripts/release-verify.sh <dir>
set -euo pipefail

readonly SIGNER="releases@kerge.io"
readonly NAMESPACE="kerge-release"

die() {
	echo "release-verify: $*" >&2
	exit 1
}

main() {
	[ $# -eq 1 ] || die "usage: release-verify.sh <dir>"
	local dir="$1" pubkey
	[ -f "$dir/install-agent.sh" ] || die "$dir has no install-agent.sh"
	pubkey="$(sed -n 's/^RELEASE_PUBKEY="\(.*\)"$/\1/p' "$dir/install-agent.sh")"
	[ -n "$pubkey" ] || die "install-agent.sh has no RELEASE_PUBKEY"

	local signers
	signers="$(mktemp)"
	# shellcheck disable=SC2064 # expand now: the variable is local
	trap "rm -f '$signers'" EXIT
	printf '%s namespaces="%s" %s\n' "$SIGNER" "$NAMESPACE" "$pubkey" >"$signers"
	ssh-keygen -Y verify -f "$signers" -I "$SIGNER" -n "$NAMESPACE" \
		-s "$dir/checksums.txt.sig" <"$dir/checksums.txt" ||
		die "checksums.txt.sig is not a signature by the key in install-agent.sh"

	(cd "$dir" && sha256sum -c checksums.txt) || die "a file does not match checksums.txt"
	echo "release-verify: the signature and every checksum are valid"
}

main "$@"
