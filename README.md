<div align="center">

# portassh

**Remote SSH access to Windows machines without admin rights, installation or setup — one exe, run it and connect**

[![CI](https://github.com/safehome85/portassh/actions/workflows/ci.yml/badge.svg)](https://github.com/safehome85/portassh/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/Go-1.21%2B-00ADD8?logo=go&logoColor=white)
![Platform](https://img.shields.io/badge/Windows-10%2F11%20x64%20%7C%20x86%20%7C%20ARM64-0078D4?logo=windows&logoColor=white)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

*Remote support for users, fleet administration, access to your own machine from anywhere*

</div>

---

## The scenario

You need a terminal on a Windows machine that:
- sits behind NAT or a restrictive firewall (no direct access),
- belongs to a user **without admin rights**,
- must **not have anything installed** on it,
- and whose antivirus distrusts unknown programs.

The answer: one portable exe. Run it — it starts an SSH server on loopback
and dials a reverse tunnel through your relay server. From your computer:

```sh
ssh -o StrictHostKeyChecking=accept-new -J root@<relay> -p 2200 anyuser@localhost
```

Zero passwords, zero config files on the target, no firewall prompts.

## How it works

```
 your PC                                relay (VPS, any SSH)                target Windows
┌─────────┐   ssh -J   ┌─────────────────────────┐  reverse tunnel    ┌───────────────────────┐
│ client  ├───────────►│  sshd :22               │◄────SSH session────┤ portassh.exe          │
│         │  direct-   │  127.0.0.1:2200 ────────┼────────────────────┤ • sshd  127.0.0.1:2222│
└─────────┘  tcpip     │  (port forward)         │                    │ • cmd.exe via ConPTY  │
                       └─────────────────────────┘                    └───────────────────────┘
```

- On the target, the exe runs an **embedded SSH server** (shell / exec / SFTP)
  on loopback only — the firewall never prompts and no admin rights are needed
- In parallel it opens a **reverse tunnel** to the relay using a key baked in
  at build time — no inbound access to the target is required
- All keys and addresses are baked into the exe at build time (`baked.go`)

## Features

| | |
|---|---|
| 🖥 **Interactive shell** | cmd.exe / powershell via ConPTY ([go-pty]), full terminal with resize |
| ⚡ **Exec** | the client's raw command line reaches cmd 1:1 — quotes, pipes and backslashes survive |
| 📁 **SFTP** | `scp`/`sftp` files over the same tunnel |
| 🈳 **UTF-8** | CP866 output is decoded on the fly, no chcp spawning |
| 🔁 **Resilience** | auto-reconnect (backoff 5→30 s), spawn retry on antivirus blocks, panic isolation |
| 🖥 **Multi-machine** | registry on the relay, automatic port allocation, up to 100 machines at once |
| 🔐 **Admin mode** | `portassh.exe -admin` — one UAC prompt, separate tunnel, shell running as administrator |
| 🤫 **Stealth** | no window appears, status goes to `portassh.log`; child consoles never flash |
| 📦 **One file** | the 386 build runs on Win10/11 x64 (WOW64), x86 and ARM64 (x86 emulation) |

## Quick start

**1. Relay** — any SSH server (a VPS will do). The tunnel user only needs
TCP forwarding:

```sh
useradd -m -s /bin/sh portassh
mkdir -p /home/portassh/.ssh && chmod 700 /home/portassh/.ssh
```

**2. Keys and build** (requires Go 1.21+ and `ssh-keygen`):

```sh
./scripts/gen-baked.sh relay.example.com   # ed25519 keys + baked.go
./build.sh                                 # portassh.exe
```

The script prints a line for the relay's `authorized_keys` — add it once:

```sh
echo "<relay_ed25519.pub>" > /home/portassh/.ssh/authorized_keys
```

**3. Target machine:** copy `portassh.exe` over and double-click it. Done —
connect using the command from step 1 or from `portassh_connection.txt`
(the file appears next to the exe).

> Full instructions: [docs/configuration.md](docs/configuration.md)

## Admin access

```sh
portassh.exe -admin        # on the target: one UAC prompt
pconnect-admin             # from your PC: a shell running as administrator
```

The admin instance keeps its own tunnel (port 2201) and does not interfere
with the regular one. See [docs/configuration.md](docs/configuration.md).

## Multiple machines

Every instance registers itself on the relay: `PC_NAME port time role`.
The port is allocated automatically: 2200, 2201, … up to 100 machines.

```sh
pconnect-list              # all machines online
pconnectgo DESKTOP-ABC123  # connect to a specific one by name
```

[docs/configuration.md](docs/configuration.md) •
[docs/troubleshooting.md](docs/troubleshooting.md)

## Security

The trust model is simple and explicit:

- **The exe contains the tunnel private key** — anyone holding the binary can
  open tunnels through your relay. The relay shell stays out of reach (the
  tunnel user is restricted). Never publish built binaries
- **The embedded server accepts exactly one baked client key** and listens on
  loopback only
- **The relay host key is pinned at build time** — no TOFU, no MITM window
- Vulnerability reports: [SECURITY.md](SECURITY.md)

## Antivirus

An unsigned exe that spawns processes and opens tunnels attracts heuristics
(verified against Kaspersky). portassh minimises the triggers (manifest
`asInvoker`, no extra process spawns, retry on blocks), but the complete fix
is marking it as a trusted app in your antivirus or code signing.
Step by step: [docs/troubleshooting.md](docs/troubleshooting.md#forkexec-cmdexe-access-is-denied-intermittent).

## Troubleshooting

Everything that broke during months of live use, with fixes, lives in
[docs/troubleshooting.md](docs/troubleshooting.md): intermittent
`Access is denied`, ghost ports on the relay, Cyrillic encoding, the 8191
command length limit, relay-side diagnostics.

## Repository layout

```
├── main.go              # core: server, tunnel, exec
├── oem_windows.go       # CP866→UTF-8 decoding and SysProcAttr.CmdLine (Windows)
├── elevate_windows.go   # -admin: UAC via ShellExecuteW
├── baked.go.example     # configuration template (keys/relay)
├── scripts/gen-baked.sh # key generation and baked.go writer
├── build.sh             # build + optional code signing
├── docs/                # configuration and troubleshooting guides
└── .github/             # CI, issue/PR templates, dependabot
```

## Development

```sh
go vet ./... && go test ./...   # e2e: a real PTY through the embedded server
```

CI runs tests on Ubuntu and macOS, builds a Windows artifact and
cross-compiles for amd64/386/arm64. CI builds use generated throwaway keys —
real keys are not needed and must never reach CI.

## Credits

- [gliderlabs/ssh](https://github.com/gliderlabs/ssh) — the SSH server
- [go-pty](https://github.com/aymanbagabas/go-pty) — ConPTY/PTY (Windows support first appeared here; creack/pty has none)
- [pkg/sftp](https://github.com/pkg/sftp) — SFTP
- [golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto/ssh) — the tunnel's SSH client

## License

[MIT](LICENSE)

[go-pty]: https://github.com/aymanbagabas/go-pty
