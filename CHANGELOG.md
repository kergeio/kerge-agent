# Changelog

Notable changes to the Kerge agent. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions
follow [Semantic Versioning](https://semver.org/). Release candidates
are not listed separately.

## [Unreleased]

The first release, v0.1.0.

### Added

- Metrics every 5 seconds (3 to 60, `INTERVAL`): CPU usage, memory and
  swap, load average, the root filesystem, byte counters per network
  interface, and uptime; host details (hostname, OS, kernel, architecture,
  CPU model and cores) once per connection.
- Interfaces left out of reports by name pattern (`IFACE_EXCLUDE`), with a
  default list of loopback, container, virtual machine and tunnel
  interfaces.
- Every collector runs with its own timeout, at most one at a time, so a
  hung filesystem or a busy host leaves single values empty instead of
  stopping reports; a report that cannot be sent is replaced by the next,
  never queued.
- One outbound WebSocket connection to the panel, protocol `kerge.v1`:
  registration with a one-time token, then a long-term credential stored
  in the state directory; reconnection with exponential backoff and
  jitter.
- `install-agent.sh`: installs, reinstalls and uninstalls the agent as a
  systemd service running as the unprivileged user `kerge`, after checking
  the OpenSSH signature of the release's checksums and the archive's
  checksum.
- Releases for Linux amd64 and arm64 as archives holding the binary and
  the license texts of all code in it (installed to
  `/usr/share/doc/kerge-agent/`), with `checksums.txt` signed by the
  release key.
