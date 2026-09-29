#!/bin/sh
# Fails if any first-party package the agent binary depends on imports
# os/exec. This covers both this module and kerge-protocol, since both
# are our own code and both end up in the agent binary. Test files are not
# part of the binary and are not checked.
set -eu

failed=0

for pkg in $(go list -deps ./cmd/agent | grep -E '^github\.com/kergeio/'); do
	if go list -f '{{range .Imports}}{{println .}}{{end}}' "$pkg" | grep -qx 'os/exec'; then
		echo "agent package $pkg imports os/exec"
		failed=1
	fi
done

if [ "$failed" -ne 0 ]; then
	echo "agent code must not import os/exec"
	exit 1
fi
