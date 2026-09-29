# Contributing

Thanks for your interest in Kerge.

## Licensing

This repository is licensed under the Apache License 2.0 (see `LICENSE`).
Contributions are accepted under the same license.

## Developer Certificate of Origin

Every commit must carry a `Signed-off-by` line certifying the Developer
Certificate of Origin (see the `DCO` file):

```
git commit -s
```

The line must match the author of the commit:

```
Signed-off-by: Jane Doe <jane@example.com>
```

CI rejects a change when any of its commits lacks this line.

There is no CLA to sign.

## What belongs here

This repository holds the agent: metric collection, the configuration
file, and the connection to the panel.

The wire protocol and everything that defines how the reported data is to
be read live in [github.com/kergeio/kerge-protocol](https://github.com/kergeio/kerge-protocol).
A change to what the agent reports usually starts there.

Support for further platforms is welcome. The collection layer is behind
a platform-independent interface, so a new platform means implementing
that interface, not touching the rest.

## Hard rules

- The agent must not listen on any port.
- The agent must not start a process. Its own packages must not import
  `os/exec`; `make lint` checks this over everything that ends up in the
  binary, including `github.com/kergeio/kerge-protocol`.
- Panel messages are parsed against a whitelist with strict decoding. No
  dynamic evaluation of anything the panel sends, ever.

## Before opening a pull request

```
make check
```

That runs formatting, `go vet`, the tests with the race detector, a
vulnerability scan, and the checks described below.

## House rules

- Everything in this repository is written in English: code, comments,
  commit messages, branch names and documentation.
- Commit messages follow Conventional Commits, for example
  `feat(protocol): add the host_info interval field`.
- Changes to the wire format belong in the protocol repository, not
  here.
