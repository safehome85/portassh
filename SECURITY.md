# Security Policy

portassh is a remote-access tool: security questions matter more than usual
here, so please report vulnerabilities responsibly.

## How to report a vulnerability

Use the **Report a vulnerability** button (Security → Advisories) in this
repository — a private channel. **Do not open a public issue** with details of
an exploitable problem.

Expected response time: 3 days. Coordinated disclosure: 90 days.

## Areas of greatest interest

- `baked.go` / the built exe: they contain the tunnel private key and the
  client key
- Session isolation: one session must never affect the tunnel or other
  sessions
- Embedded server authentication: client key matching
- TOFU/FixedHostKey of the relay connection

## What is NOT considered a vulnerability

- The exe leaking into third parties: the tunnel key inside the binary is a
  deliberate design trade-off (see README, Security section). A compromised
  binary = a compromised tunnel; the relay account is limited to port
  forwarding
- Antivirus blocks on the unsigned binary

## Supported versions

The latest version of `main`.
