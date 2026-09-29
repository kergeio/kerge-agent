#!/bin/bash
# Installs the Kerge agent on a Linux host.
#
# The panel prints the command that downloads and runs this script. Every
# download is verified twice before anything is installed: the checksums
# file carries an OpenSSH signature made with the release key, and the
# binary must match its checksum. Neither check can be skipped.
#
# Usage:
#   install-agent.sh --server wss://panel.example.com/api/agent/ws --token <token>
#   install-agent.sh --version 0.3.1
#   install-agent.sh --uninstall
#
# VERSION is rewritten when the release workflow uploads this script, so a
# script downloaded from release vX installs agent vX; the script in the
# repository carries no version and refuses to install anything. BASE_URL
# is rewritten only by the tests.
set -euo pipefail

VERSION="dev"
BASE_URL="https://github.com/kergeio/kerge-agent/releases/download"
RELEASE_PUBKEY="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMMWMoTwuKCmpyWran5GZp5KiZuOIt6N/vzcfZsrdfpH kerge-release"

readonly SIGNER="releases@kerge.io"
readonly NAMESPACE="kerge-release"
readonly SERVICE="kerge-agent"
readonly RUN_AS="kerge"
readonly BIN_PATH="/usr/local/bin/kerge-agent"
readonly CONF_DIR="/etc/kerge-agent"
readonly CONF_PATH="$CONF_DIR/agent.conf"
readonly UNIT_PATH="/etc/systemd/system/kerge-agent.service"
readonly STATE_DIR="/var/lib/kerge-agent"

server=""
token=""
uninstall=false
workdir=""

main() {
	parse_args "$@"
	need_root
	if [ "$uninstall" = true ]; then
		do_uninstall
		return
	fi
	do_install
}

die() {
	echo "install-agent: $*" >&2
	exit 1
}

info() { echo "install-agent: $*"; }

usage() {
	cat <<'USAGE'
Usage:
  install-agent.sh --server <wss url> --token <enrollment token> [--version <v>]
  install-agent.sh [--version <v>]        reinstall, keeping the configuration
  install-agent.sh --uninstall            remove the agent, its files and its user
USAGE
}

parse_args() {
	while [ $# -gt 0 ]; do
		case "$1" in
		--server)
			[ $# -ge 2 ] || die "--server needs a value"
			server="$2"
			shift 2
			;;
		--token)
			[ $# -ge 2 ] || die "--token needs a value"
			token="$2"
			shift 2
			;;
		--version)
			[ $# -ge 2 ] || die "--version needs a value"
			VERSION="${2#v}"
			shift 2
			;;
		--uninstall)
			uninstall=true
			shift
			;;
		-h | --help)
			usage
			exit 0
			;;
		*)
			usage >&2
			die "unknown option $1"
			;;
		esac
	done
}

need_root() {
	[ "$(id -u)" = "0" ] || die "run this as root, for example with sudo"
}

# detect_arch maps the machine to the name the release assets use. v1
# publishes linux amd64 and arm64 only.
detect_arch() {
	local os machine
	os="$(uname -s)"
	[ "$os" = "Linux" ] || die "the agent runs on Linux only, not on $os"
	machine="$(uname -m)"
	case "$machine" in
	x86_64 | amd64) echo "amd64" ;;
	aarch64 | arm64) echo "arm64" ;;
	*) die "unsupported architecture $machine; the agent is published for amd64 and arm64" ;;
	esac
}

require_tools() {
	local missing=()
	local tool
	for tool in curl sha256sum systemctl useradd groupadd install getent sed grep; do
		command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
	done
	[ ${#missing[@]} -eq 0 ] || die "these commands are missing: ${missing[*]}"

	# The signature check is mandatory, so a host that cannot verify one
	# does not get an agent.
	command -v ssh-keygen >/dev/null 2>&1 ||
		die "ssh-keygen is missing; install openssh-client (OpenSSH 8.1 or newer) and run this again"
	if ssh-keygen -Y verify 2>&1 | grep -qi 'unknown option'; then
		die "this ssh-keygen cannot verify signatures; install OpenSSH 8.1 or newer and run this again"
	fi
}

cleanup() {
	if [ -n "$workdir" ]; then
		rm -rf "$workdir"
	fi
}

do_install() {
	[ "$VERSION" != "dev" ] ||
		die "this script carries no release version; run it with --version <v> or use the command the panel shows"

	local arch asset
	arch="$(detect_arch)"
	asset="kerge-agent-linux-$arch"
	require_tools

	if [ ! -f "$CONF_PATH" ]; then
		[ -n "$server" ] || die "--server is required for a new installation"
		[ -n "$token" ] || die "--token is required for a new installation"
	fi
	if [ -n "$server" ]; then
		case "$server" in
		ws://* | wss://*) ;;
		*) die "--server must be a ws:// or wss:// URL, not $server" ;;
		esac
	fi

	workdir="$(mktemp -d)"
	trap cleanup EXIT
	download "$asset"
	verify_signature
	verify_checksum "$asset"

	create_user
	info "installing $asset $VERSION"
	install -o root -g root -m 0755 "$workdir/$asset" "$BIN_PATH"
	write_config
	write_unit
	start_service
	info "the agent is installed and running; the panel shows the host as enrolled once it connects"
}

download() {
	local asset="$1" url="$BASE_URL/v$VERSION"
	local name
	for name in "$asset" checksums.txt checksums.txt.sig; do
		curl -fsSL -o "$workdir/$name" "$url/$name" ||
			die "could not download $url/$name"
	done
}

# verify_signature checks that the checksums file was signed with the
# release key. The key is part of this script, so a tampered mirror cannot
# supply its own.
verify_signature() {
	printf '%s namespaces="%s" %s\n' "$SIGNER" "$NAMESPACE" "$RELEASE_PUBKEY" >"$workdir/allowed_signers"
	ssh-keygen -Y verify -f "$workdir/allowed_signers" -I "$SIGNER" -n "$NAMESPACE" \
		-s "$workdir/checksums.txt.sig" <"$workdir/checksums.txt" >/dev/null ||
		die "the signature of checksums.txt does not match the release key; nothing was installed"
	info "the release signature is valid"
}

# verify_checksum compares the downloaded binary with the line for it in
# the signed checksums file.
verify_checksum() {
	local asset="$1"
	grep -E "[ *]$asset\$" "$workdir/checksums.txt" >"$workdir/expected" ||
		die "checksums.txt has no entry for $asset"
	(cd "$workdir" && sha256sum -c --quiet expected) ||
		die "the checksum of $asset does not match; nothing was installed"
	info "the checksum of $asset is valid"
}

create_user() {
	if ! getent group "$RUN_AS" >/dev/null; then
		groupadd --system "$RUN_AS"
	fi
	if getent passwd "$RUN_AS" >/dev/null; then
		return
	fi
	local shell="/bin/false"
	local candidate
	for candidate in /usr/sbin/nologin /sbin/nologin; do
		if [ -x "$candidate" ]; then
			shell="$candidate"
			break
		fi
	done
	info "creating the system user $RUN_AS"
	useradd --system --no-create-home --home-dir "$STATE_DIR" --gid "$RUN_AS" --shell "$shell" "$RUN_AS"
}

# write_config creates agent.conf, or updates the keys this script manages
# and leaves the rest of an existing file untouched.
write_config() {
	mkdir -p "$CONF_DIR"
	local tmp="$workdir/agent.conf"
	if [ -f "$CONF_PATH" ]; then
		cp "$CONF_PATH" "$tmp"
		if [ -n "$server" ]; then
			set_key "$tmp" SERVER "$server"
		fi
		if [ -n "$token" ]; then
			set_key "$tmp" TOKEN "$token"
		fi
	else
		cat >"$tmp" <<-CONF
			# Kerge agent configuration. See the agent repository for every key.
			SERVER=$server
			TOKEN=$token
			# INTERVAL: seconds between reports, 3 to 60. The default is 5.
			# INTERVAL=5
			# IFACE_EXCLUDE: comma-separated patterns of interfaces to leave out.
			# Leaving the line out uses the built-in list; an empty value excludes nothing.
			# IFACE_EXCLUDE=
		CONF
	fi
	install -o root -g "$RUN_AS" -m 0640 "$tmp" "$CONF_PATH"
}

# set_key replaces the value of one key in a config file, appending the
# line when the key is not there yet.
set_key() {
	local file="$1" key="$2" value="$3"
	if grep -q "^$key=" "$file"; then
		local escaped
		escaped="$(printf '%s' "$value" | sed -e 's/[&|\\]/\\&/g')"
		sed -i "s|^$key=.*|$key=$escaped|" "$file"
	else
		printf '%s=%s\n' "$key" "$value" >>"$file"
	fi
}

# write_unit installs the service definition. It is written out here
# because this script is downloaded on its own; deploy/kerge-agent.service
# in the agent repository is the same file, and a test compares the two.
write_unit() {
	cat >"$workdir/unit" <<-'UNIT'
		[Unit]
		Description=Kerge Agent
		After=network-online.target
		Wants=network-online.target

		[Service]
		User=kerge
		Nice=10
		CPUWeight=20
		IOWeight=20
		MemoryMax=64M
		OOMScoreAdjust=-100
		ExecStart=/usr/local/bin/kerge-agent --config /etc/kerge-agent/agent.conf
		Restart=on-failure
		RestartSec=5
		StateDirectory=kerge-agent
		KeyringMode=private
		LockPersonality=yes
		NoNewPrivileges=yes
		ProtectClock=yes
		ProtectHome=read-only
		ProtectHostname=yes
		ProtectKernelLogs=yes
		ProtectSystem=strict
		RemoveIPC=yes
		RestrictSUIDSGID=true

		[Install]
		WantedBy=multi-user.target
	UNIT
	install -o root -g root -m 0644 "$workdir/unit" "$UNIT_PATH"
}

start_service() {
	systemctl daemon-reload
	systemctl enable --now "$SERVICE"
	# A reinstall replaces a binary that is already running, so the
	# service is restarted whether or not it was up before.
	systemctl restart "$SERVICE"
}

do_uninstall() {
	info "removing the Kerge agent"
	systemctl disable --now "$SERVICE" 2>/dev/null || true
	rm -f "$UNIT_PATH"
	systemctl daemon-reload 2>/dev/null || true
	rm -f "$BIN_PATH"
	rm -rf "$CONF_DIR" "$STATE_DIR"
	if getent passwd "$RUN_AS" >/dev/null; then
		userdel "$RUN_AS" 2>/dev/null || true
	fi
	if getent group "$RUN_AS" >/dev/null; then
		groupdel "$RUN_AS" 2>/dev/null || true
	fi
	info "the agent, its configuration, its state and its user are gone"
}

main "$@"
