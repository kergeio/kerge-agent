# Kerge agent

The monitoring agent of [Kerge](https://kerge.io). It collects host
metrics and reports them to a Kerge panel over a single outbound
WebSocket connection.

## Status

Pre-release. Release candidates (`v0.1.0-rc.N`) are published for
testing; the first release is v0.1.0.

## What it does, and what it cannot do

- It only ever dials out. It listens on no port.
- The panel can send it exactly two things: the result of registration,
  and an error that ends the connection. There is no command message, and
  the agent parses panel messages against a whitelist.
- It never starts a process. The agent's own packages must not import
  `os/exec`, and CI enforces that.
- It does not update itself. A new version is installed by running the
  install script again.

## Supported platforms

Linux on amd64 and arm64, with systemd. The install script also needs
`curl`, `sha256sum` and `ssh-keygen` from OpenSSH 8.1 or newer (package
`openssh-client` on Debian and Ubuntu), and root.

## Installation

In the panel, choose **Add host**. The panel shows a command for that
host; run it on the host. It always has this form:

```
f=$(mktemp) && curl -fsSL -o "$f" https://github.com/kergeio/kerge-agent/releases/download/v<version>/install-agent.sh \
  && echo "<sha256 of install-agent.sh>  $f" | sha256sum -c --quiet - \
  && sudo bash "$f" --server wss://panel.example.com/api/agent/ws --token <one-time token>; rm -f "$f"
```

The version and the sha256 are compiled into the panel, so a panel
release installs exactly one agent release, and the script is checked
before it runs. The token can be used once.

`install-agent.sh` then:

1. downloads the agent binary and `checksums.txt` of the same release,
   and stops unless `checksums.txt` carries a valid OpenSSH signature by
   the release key built into the script, and the binary matches it;
2. creates the system user `kerge` (no login shell, no home directory);
3. writes `/etc/kerge-agent/agent.conf` (owner `root:kerge`, mode 640);
4. installs `/usr/local/bin/kerge-agent` and the systemd unit
   `kerge-agent.service`, and starts it.

Running the script again reinstalls the agent and keeps
`agent.conf`; `--server` and `--token` replace those two values when
given. Other options:

| Option | Effect |
|---|---|
| `--version <v>` | install that release instead of the script's own |
| `--uninstall` | stop the service and remove the binary, the configuration, the state directory and the `kerge` user |

### sha256 of install-agent.sh

Each release lists it in its release notes. For the releases so far:

| Release | sha256 of `install-agent.sh` |
|---|---|
| none yet | |

### The release key

Every release's `checksums.txt` is signed with this key (namespace
`kerge-release`), which `install-agent.sh` carries:

```
ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMMWMoTwuKCmpyWran5GZp5KiZuOIt6N/vzcfZsrdfpH
```

Fingerprint `SHA256:o8FwbEF+/tiVjyIxLZp+Pg5WlDcSu5M5dJgY5WgwMiM`.

## Manual installation

To install without the script, on a host of either architecture
(`amd64` below):

```
v=0.1.0
base=https://github.com/kergeio/kerge-agent/releases/download/v$v
curl -fsSLO "$base/kerge-agent-linux-amd64" -O "$base/checksums.txt" -O "$base/checksums.txt.sig"

# The signature, then the binary.
echo 'releases@kerge.io namespaces="kerge-release" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMMWMoTwuKCmpyWran5GZp5KiZuOIt6N/vzcfZsrdfpH' > allowed_signers
ssh-keygen -Y verify -f allowed_signers -I releases@kerge.io -n kerge-release -s checksums.txt.sig < checksums.txt
sha256sum -c --ignore-missing checksums.txt

sudo install -o root -g root -m 0755 kerge-agent-linux-amd64 /usr/local/bin/kerge-agent
sudo useradd --system --no-create-home --shell /usr/sbin/nologin kerge
```

Then write `/etc/kerge-agent/agent.conf` (see below; owner `root:kerge`,
mode 640), install [`deploy/kerge-agent.service`](deploy/kerge-agent.service)
as `/etc/systemd/system/kerge-agent.service`, and run
`sudo systemctl enable --now kerge-agent`.

## Configuration

`/etc/kerge-agent/agent.conf` holds one `KEY=value` per line. Lines
starting with `#` and empty lines are ignored; values are taken as they
are, without quotes or variable expansion. An unknown key, a repeated key
or a malformed line stops the agent with an error.

```
SERVER=wss://panel.example.com/api/agent/ws
TOKEN=<one-time enrollment token>
INTERVAL=5
IFACE_EXCLUDE=lo,docker*,veth*,br-*,cni*,flannel*,cali*,kube-*,virbr*,vnet*,tun*,tap*,wg*,tailscale*,zt*,dummy*
```

| Key | Meaning |
|---|---|
| `SERVER` | the panel's agent endpoint. `ws://` is accepted for loopback addresses only. |
| `TOKEN` | the one-time token from the panel, used for the first registration only |
| `INTERVAL` | seconds between reports, 3 to 60; default 5 |
| `IFACE_EXCLUDE` | comma-separated patterns of network interfaces the agent does not report. Without the line, the list above applies; `IFACE_EXCLUDE=` (empty) leaves out none. Patterns match whole names, are case-sensitive, and support `*` only. |

A bonded or teamed interface and its member interfaces count the same
traffic twice. Exclude one side: either the members (for example
`eth0,eth1`) or the bond (for example `bond0`). The panel applies its own
exclusion rules on top, set in its settings and per host.

Restart the agent after editing the file:
`sudo systemctl restart kerge-agent`.

### Registration and the credential

On its first connection the agent exchanges the token for a long-term
credential and stores it in `/var/lib/kerge-agent/credential` (mode
600). From then on it uses the credential; the token is spent. When the
panel resets a host's access, it revokes the credential: run the command
the panel then shows, and the agent registers again with the new token.

## Command line

```
kerge-agent [--config path] [--state-dir path]
kerge-agent --version
```

| Flag | Default |
|---|---|
| `--config` | `/etc/kerge-agent/agent.conf` |
| `--state-dir` | `$STATE_DIRECTORY` (set by systemd), else `/var/lib/kerge-agent` |

The agent logs to standard error: `journalctl -u kerge-agent`.

## Protocol

The wire protocol and its reference implementation live in
[github.com/kergeio/kerge-protocol](https://github.com/kergeio/kerge-protocol);
see its `PROTOCOL.md`.

## License

Apache License 2.0. See `LICENSE` and `NOTICE`.
