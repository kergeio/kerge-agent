#!/bin/sh
# Fails if the agent module depends on the panel module, directly or
# through another module. The agent and the panel share only the protocol
# module; the panel's code stays under its own license.
set -eu

readonly PANEL="github.com/kergeio/kerge-panel"

cd "$(dirname "$0")/.."
# The versions in go.mod, not a local workspace that also holds the panel.
mods="$(GOWORK=off go list -m all)"
if printf '%s\n' "$mods" | grep -q "^$PANEL\( \|\$\)"; then
	echo "check-deps: the agent must not depend on $PANEL" >&2
	exit 1
fi
