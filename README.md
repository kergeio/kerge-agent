# Kerge agent

The monitoring agent of [Kerge](https://kerge.io). It collects host
metrics and reports them to a Kerge panel over a single outbound
WebSocket connection.

## Status

Pre-release. Installation instructions arrive with the first release.

## What it does, and what it cannot do

- It only ever dials out. It listens on no port.
- The panel can send it exactly two things: the result of registration,
  and an error that ends the connection. There is no command message, and
  the agent parses panel messages against a whitelist.
- It never starts a process. The agent's own packages must not import
  `os/exec`, and CI enforces that.

## Installation

The panel generates the install command for a host.

## Protocol

The wire protocol and its reference implementation live in
[github.com/kergeio/kerge-protocol](https://github.com/kergeio/kerge-protocol).

## License

Apache License 2.0. See `LICENSE` and `NOTICE`.
