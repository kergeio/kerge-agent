# Kerge agent

The monitoring agent of [Kerge](https://kerge.io).
It collects host metrics and reports them to a Kerge panel over the
[Kerge protocol](https://github.com/kergeio/kerge-protocol).

- Only connects out; listens on no port
- Runs no commands: the panel cannot make it do anything
- Never updates itself

## Install

In the panel, choose **Add host** and run the command it shows on the
host. Linux on amd64 or arm64 with systemd.

To uninstall:

```
curl -fsSL https://github.com/kergeio/kerge-agent/releases/latest/download/install-agent.sh | sudo bash -s -- --uninstall
```

## Configuration

`/etc/kerge-agent/agent.conf`:

```
SERVER=wss://panel.example.com/api/agent/ws
TOKEN=<one-time enrollment token>
INTERVAL=5
IFACE_EXCLUDE=lo,docker*,veth*,br-*,cni*,flannel*,cali*,kube-*,virbr*,vnet*,tun*,tap*,wg*,tailscale*,zt*,dummy*
```

| Key | Meaning |
|---|---|
| `SERVER` | the panel's agent endpoint |
| `TOKEN` | the one-time token from the panel, used for the first registration only |
| `INTERVAL` | seconds between reports, 3 to 60; default 5 |
| `IFACE_EXCLUDE` | network interfaces not to report, comma-separated, `*` as wildcard; default as above |

Restart after editing: `sudo systemctl restart kerge-agent`.

## Command line

```
kerge-agent [--config path] [--state-dir path]
kerge-agent --version
```

| Flag | Default |
|---|---|
| `--config` | `/etc/kerge-agent/agent.conf` |
| `--state-dir` | `/var/lib/kerge-agent` |

Logs: `journalctl -u kerge-agent`.

## License

Apache-2.0; see `LICENSE` and `NOTICE`.
