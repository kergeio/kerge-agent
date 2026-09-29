#!/bin/bash
# One case of the install-agent.sh test suite, run inside a throwaway
# container by scripts/test-install-agent.sh. The repository is mounted at
# /src, read only.
#
# The release the installer downloads is built here: an archive with a fake
# binary and license texts, a checksums file and an OpenSSH signature over
# it. Most cases run a copy of
# the installer whose version, download base and release key point at that
# throwaway release; case_release_key instead keeps the key the script
# ships with.
set -euo pipefail

readonly RELEASE_VERSION="1.2.3"
readonly RELEASE_DIR="/release/v$RELEASE_VERSION"
readonly INSTALLER="/work/install-agent.sh"
readonly SYSTEMCTL_LOG="/work/systemctl.log"
readonly DOC_DIR="/usr/share/doc/kerge-agent"

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

pass() {
	echo "ok: $*"
}

arch() {
	case "$(uname -m)" in
	x86_64 | amd64) echo amd64 ;;
	aarch64 | arm64) echo arm64 ;;
	*) fail "the test image runs on an unsupported architecture: $(uname -m)" ;;
	esac
}

# stub_systemctl replaces systemd with a log, because a container has no
# service manager. Everything else in the installer is real.
stub_systemctl() {
	cat >/usr/local/sbin/systemctl <<-STUB
		#!/bin/sh
		echo "\$@" >> $SYSTEMCTL_LOG
		exit 0
	STUB
	chmod 0755 /usr/local/sbin/systemctl
	: >"$SYSTEMCTL_LOG"
}

asset() {
	echo "kerge-agent-linux-$(arch).tar.gz"
}

# build_release writes the assets the installer downloads and signs them
# with a key generated for this run. The archive holds the given files;
# by default all four a release has.
build_release() {
	local sign_key="${1:-/work/release_key}"
	shift || true
	local members=("$@")
	[ ${#members[@]} -gt 0 ] || members=(kerge-agent LICENSE NOTICE THIRD_PARTY_LICENSES)
	mkdir -p "$RELEASE_DIR" /work/pkg
	printf '#!/bin/sh\necho "kerge-agent %s"\n' "$RELEASE_VERSION" >/work/pkg/kerge-agent
	chmod 0755 /work/pkg/kerge-agent
	for doc in LICENSE NOTICE THIRD_PARTY_LICENSES; do
		echo "the $doc text" >"/work/pkg/$doc"
	done
	tar -czf "$RELEASE_DIR/$(asset)" -C /work/pkg "${members[@]}"
	(cd "$RELEASE_DIR" && sha256sum "$(asset)" >checksums.txt)

	ssh-keygen -q -t ed25519 -N '' -C kerge-release -f /work/release_key
	if [ "$sign_key" != "/work/release_key" ]; then
		ssh-keygen -q -t ed25519 -N '' -C attacker -f "$sign_key"
	fi
	ssh-keygen -Y sign -q -f "$sign_key" -n kerge-release "$RELEASE_DIR/checksums.txt"
}

# prepare_installer copies the installer with the values a release would
# carry: its version, where its assets live and the public release key.
prepare_installer() {
	local pubkey
	pubkey="$(cat /work/release_key.pub)"
	sed -e "s|^VERSION=.*|VERSION=\"$RELEASE_VERSION\"|" \
		-e "s|^BASE_URL=.*|BASE_URL=\"file:///release\"|" \
		-e "s|^RELEASE_PUBKEY=.*|RELEASE_PUBKEY=\"$pubkey\"|" \
		/src/scripts/install-agent.sh >"$INSTALLER"
	chmod 0755 "$INSTALLER"
}

setup() {
	mkdir -p /work
	stub_systemctl
	build_release "$@"
	prepare_installer
}

# assert_not_installed is what every rejected download must leave behind:
# nothing.
assert_not_installed() {
	[ ! -e /usr/local/bin/kerge-agent ] || fail "a binary was installed although the check failed"
	[ ! -e /etc/kerge-agent/agent.conf ] || fail "a configuration was written although the check failed"
	[ ! -e /etc/systemd/system/kerge-agent.service ] || fail "a unit was written although the check failed"
	[ ! -e "$DOC_DIR" ] || fail "license texts were installed although the check failed"
	[ ! -s "$SYSTEMCTL_LOG" ] || fail "systemctl was called although the check failed: $(cat "$SYSTEMCTL_LOG")"
}

assert_mode() {
	local path="$1" want="$2" got
	got="$(stat -c '%a' "$path")"
	[ "$got" = "$want" ] || fail "$path has mode $got, want $want"
}

assert_owner() {
	local path="$1" want="$2" got
	got="$(stat -c '%U:%G' "$path")"
	[ "$got" = "$want" ] || fail "$path is owned by $got, want $want"
}

assert_installed() {
	assert_mode /usr/local/bin/kerge-agent 755
	assert_owner /usr/local/bin/kerge-agent root:root
	assert_mode /etc/kerge-agent/agent.conf 640
	assert_owner /etc/kerge-agent/agent.conf root:kerge
	assert_mode /etc/systemd/system/kerge-agent.service 644
	local doc
	for doc in LICENSE NOTICE THIRD_PARTY_LICENSES; do
		assert_mode "$DOC_DIR/$doc" 644
		assert_owner "$DOC_DIR/$doc" root:root
		cmp -s "/work/pkg/$doc" "$DOC_DIR/$doc" || fail "$DOC_DIR/$doc is not the file from the archive"
	done
	cmp -s /work/pkg/kerge-agent /usr/local/bin/kerge-agent || fail "the installed binary is not the one from the archive"

	# The unit the installer writes and the one in the repository are the
	# same file.
	diff /src/deploy/kerge-agent.service /etc/systemd/system/kerge-agent.service ||
		fail "the installed unit differs from deploy/kerge-agent.service"

	grep -q '^daemon-reload$' "$SYSTEMCTL_LOG" || fail "systemd was not reloaded"
	grep -q '^enable --now kerge-agent$' "$SYSTEMCTL_LOG" || fail "the service was not enabled"
	grep -q '^restart kerge-agent$' "$SYSTEMCTL_LOG" || fail "the service was not restarted"

	getent passwd kerge >/dev/null || fail "the kerge user was not created"
	local shell
	shell="$(getent passwd kerge | cut -d: -f7)"
	case "$shell" in
	*nologin | /bin/false) ;;
	*) fail "the kerge user can log in with $shell" ;;
	esac
}

case_install() {
	setup
	"$INSTALLER" --server wss://panel.example.com/api/agent/ws --token first-token
	assert_installed
	grep -qx 'SERVER=wss://panel.example.com/api/agent/ws' /etc/kerge-agent/agent.conf ||
		fail "the server is not in the configuration: $(cat /etc/kerge-agent/agent.conf)"
	grep -qx 'TOKEN=first-token' /etc/kerge-agent/agent.conf ||
		fail "the token is not in the configuration"
	pass "a fresh host is installed"
}

# Running the installer again replaces the binary and the token the panel
# issued, and keeps what the operator edited.
case_reinstall() {
	setup
	"$INSTALLER" --server wss://panel.example.com/api/agent/ws --token first-token
	echo 'INTERVAL=9' >>/etc/kerge-agent/agent.conf
	: >"$SYSTEMCTL_LOG"

	"$INSTALLER" --token second-token
	assert_installed
	grep -qx 'TOKEN=second-token' /etc/kerge-agent/agent.conf || fail "the token was not replaced"
	grep -qx 'SERVER=wss://panel.example.com/api/agent/ws' /etc/kerge-agent/agent.conf ||
		fail "the server was lost"
	grep -qx 'INTERVAL=9' /etc/kerge-agent/agent.conf || fail "an edited setting was lost"
	[ "$(grep -c '^TOKEN=' /etc/kerge-agent/agent.conf)" = "1" ] || fail "the configuration has two tokens"
	pass "a reinstall keeps the configuration and restarts the service"
}

# A binary that does not match the signed checksums is not installed.
case_bad_checksum() {
	setup
	printf 'tampered\n' >>"$RELEASE_DIR/$(asset)"
	if "$INSTALLER" --server wss://panel.example.com/api/agent/ws --token t 2>/work/err; then
		fail "the installer accepted a binary with the wrong checksum"
	fi
	grep -q 'checksum' /work/err || fail "the error does not mention the checksum: $(cat /work/err)"
	assert_not_installed
	pass "a tampered archive is refused"
}

# Checksums signed with another key are not installed, however valid the
# signature is in itself.
# A correctly signed archive that lacks one of its files is not installed.
case_incomplete_archive() {
	setup /work/release_key kerge-agent LICENSE NOTICE
	if "$INSTALLER" --server wss://panel.example.com/api/agent/ws --token t 2>/work/err; then
		fail "the installer accepted an archive without THIRD_PARTY_LICENSES"
	fi
	grep -q 'expected files' /work/err || fail "unexpected error: $(cat /work/err)"
	assert_not_installed
	pass "an archive that lacks a file is refused"
}

case_bad_signature() {
	setup /work/attacker_key
	if "$INSTALLER" --server wss://panel.example.com/api/agent/ws --token t 2>/work/err; then
		fail "the installer accepted checksums signed with another key"
	fi
	grep -q 'signature' /work/err || fail "the error does not mention the signature: $(cat /work/err)"
	assert_not_installed
	pass "checksums signed with the wrong key are refused"
}

# Without ssh-keygen there is no way to check the signature, and the
# installer stops instead of going ahead unverified.
case_no_ssh_keygen() {
	setup
	rm -f /usr/bin/ssh-keygen
	if "$INSTALLER" --server wss://panel.example.com/api/agent/ws --token t 2>/work/err; then
		fail "the installer ran without ssh-keygen"
	fi
	grep -q 'openssh-client' /work/err || fail "the error does not say what to install: $(cat /work/err)"
	assert_not_installed
	pass "a host without ssh-keygen is refused"
}

case_unsupported_arch() {
	setup
	cat >/usr/local/bin/uname <<-'STUB'
		#!/bin/sh
		if [ "$1" = "-m" ]; then echo riscv64; else /usr/bin/uname "$@"; fi
	STUB
	chmod 0755 /usr/local/bin/uname
	if "$INSTALLER" --server wss://panel.example.com/api/agent/ws --token t 2>/work/err; then
		fail "the installer ran on an unsupported architecture"
	fi
	grep -q 'riscv64' /work/err || fail "the error does not name the architecture: $(cat /work/err)"
	assert_not_installed
	pass "an unsupported architecture is refused"
}

case_uninstall() {
	setup
	"$INSTALLER" --server wss://panel.example.com/api/agent/ws --token first-token
	mkdir -p /var/lib/kerge-agent
	echo 'credential' >/var/lib/kerge-agent/credential
	: >"$SYSTEMCTL_LOG"

	"$INSTALLER" --uninstall
	grep -q '^disable --now kerge-agent$' "$SYSTEMCTL_LOG" || fail "the service was not disabled"
	for path in /usr/local/bin/kerge-agent /etc/kerge-agent /etc/systemd/system/kerge-agent.service /var/lib/kerge-agent "$DOC_DIR"; do
		[ ! -e "$path" ] || fail "$path survived the uninstall"
	done
	! getent passwd kerge >/dev/null || fail "the kerge user survived the uninstall"
	! getent group kerge >/dev/null || fail "the kerge group survived the uninstall"
	pass "uninstalling removes the agent, its files and its user"
}

# A new host needs both values; there is no configuration to fall back on.
case_missing_arguments() {
	setup
	if "$INSTALLER" --token t 2>/work/err; then
		fail "the installer ran without a server"
	fi
	grep -q -- '--server is required' /work/err || fail "unexpected error: $(cat /work/err)"
	if "$INSTALLER" --server wss://panel.example.com/api/agent/ws 2>/work/err; then
		fail "the installer ran without a token"
	fi
	grep -q -- '--token is required' /work/err || fail "unexpected error: $(cat /work/err)"
	if "$INSTALLER" --server https://panel.example.com --token t 2>/work/err; then
		fail "the installer accepted an https server address"
	fi
	grep -q -- '--server must be' /work/err || fail "unexpected error: $(cat /work/err)"
	assert_not_installed
	pass "a new host needs a server and a token"
}

# The script in the repository carries no version, so it refuses to
# install anything before the release workflow has stamped one in.
case_unreleased_script() {
	setup
	if /src/scripts/install-agent.sh --server wss://panel.example.com/api/agent/ws --token t 2>/work/err; then
		fail "the unreleased script installed something"
	fi
	grep -q 'no release version' /work/err || fail "unexpected error: $(cat /work/err)"
	assert_not_installed
	pass "the unreleased script refuses to install"
}

# The key in the repository is the one the release workflow signs with, so
# a signature made with any other key is refused by the script as shipped.
case_release_key() {
	setup /work/attacker_key
	local shipped
	shipped="$(grep '^RELEASE_PUBKEY=' /src/scripts/install-agent.sh)"
	case "$shipped" in
	*"ssh-ed25519 AAAA"*) ;;
	*) fail "the script carries no release key: $shipped" ;;
	esac
	sed -e "s|^VERSION=.*|VERSION=\"$RELEASE_VERSION\"|" \
		-e "s|^BASE_URL=.*|BASE_URL=\"file:///release\"|" \
		/src/scripts/install-agent.sh >/work/shipped.sh
	chmod 0755 /work/shipped.sh
	if /work/shipped.sh --server wss://panel.example.com/api/agent/ws --token t 2>/work/err; then
		fail "the shipped key verified a signature it did not make"
	fi
	grep -q 'signature' /work/err || fail "unexpected error: $(cat /work/err)"
	assert_not_installed
	pass "the shipped release key rejects foreign signatures"
}

[ $# -eq 1 ] || {
	echo "usage: install-cases.sh <case>" >&2
	exit 2
}
"case_${1//-/_}"
