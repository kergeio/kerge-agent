#!/bin/bash
# Runs install-agent.sh against a throwaway Debian container, one fresh
# container per case, and checks what it leaves on the host.
#
# The container has no service manager, so systemctl is replaced by a log;
# everything else -- the download, the signature check, the checksum, the
# user, the file modes -- is the real thing. Needs Docker.
set -euo pipefail

cd "$(dirname "$0")/.."

# PLATFORM runs the cases for another architecture, for example
# linux/amd64 on an arm64 machine, which is how the asset names for both
# published architectures get exercised.
PLATFORM="${PLATFORM:-}"
# Expanded with the "+" form throughout: bash 3.2, which macOS ships,
# treats an empty array as unset under set -u.
platform_args=()
IMAGE="kerge-install-test"
if [ -n "$PLATFORM" ]; then
	platform_args=(--platform "$PLATFORM")
	IMAGE="$IMAGE-${PLATFORM//\//-}"
fi
readonly IMAGE
CASES=(
	install
	reinstall
	bad-checksum
	bad-signature
	no-ssh-keygen
	unsupported-arch
	uninstall
	missing-arguments
	unreleased-script
	release-key
)

command -v docker >/dev/null 2>&1 || {
	echo "test-install-agent: docker is required" >&2
	exit 1
}

docker build -q ${platform_args[@]+"${platform_args[@]}"} -t "$IMAGE" - >/dev/null <<'DOCKERFILE'
FROM debian:12-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl openssh-client passwd \
 && rm -rf /var/lib/apt/lists/*
DOCKERFILE

failed=0
for name in "${CASES[@]}"; do
	echo "== $name"
	if ! docker run --rm ${platform_args[@]+"${platform_args[@]}"} -v "$PWD:/src:ro" "$IMAGE" \
		bash /src/scripts/testdata/install-cases.sh "$name"; then
		echo "FAIL: $name" >&2
		failed=1
	fi
done

if [ "$failed" -ne 0 ]; then
	echo "install-agent.sh: some cases failed" >&2
	exit 1
fi
echo "install-agent.sh: all cases passed"
