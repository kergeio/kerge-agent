#!/bin/bash
# Builds the files of an agent release into an empty directory: the binary
# for every published platform, install-agent.sh with the release version
# written in, and checksums.txt over all of them. Signing checksums.txt is
# left to the caller, which holds the key.
#
# Usage: scripts/release-assets.sh <version> <out-dir>
#   <version> has no leading "v", for example 0.1.0 or 0.1.0-rc.1.
set -euo pipefail

PLATFORMS=(linux/amd64 linux/arm64)

die() {
	echo "release-assets: $*" >&2
	exit 1
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$@"
	else
		shasum -a 256 "$@"
	fi
}

main() {
	[ $# -eq 2 ] || die "usage: release-assets.sh <version> <out-dir>"
	local version="$1" out="$2"
	[[ $version =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$ ]] ||
		die "version $version is not of the form 1.2.3 or 1.2.3-rc.1"

	mkdir -p "$out"
	out="$(cd "$out" && pwd)"
	[ -z "$(ls -A "$out")" ] || die "$out is not empty"
	cd "$(dirname "$0")/.."

	local platform os arch
	for platform in "${PLATFORMS[@]}"; do
		os="${platform%/*}"
		arch="${platform#*/}"
		echo "release-assets: building kerge-agent-$os-$arch $version"
		CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
			-ldflags "-X main.version=$version" \
			-o "$out/kerge-agent-$os-$arch" ./cmd/agent
	done

	# The script in the repository carries VERSION="dev" and refuses to
	# install; the released copy installs its own release.
	sed "s/^VERSION=\"dev\"\$/VERSION=\"$version\"/" scripts/install-agent.sh >"$out/install-agent.sh"
	if [ "$(grep -c '^VERSION=' "$out/install-agent.sh")" -ne 1 ] ||
		! grep -qx "VERSION=\"$version\"" "$out/install-agent.sh"; then
		die "could not write the version into install-agent.sh"
	fi

	(cd "$out" && sha256 kerge-agent-* install-agent.sh >checksums.txt)
	echo "release-assets: done"
	cat "$out/checksums.txt"
}

main "$@"
