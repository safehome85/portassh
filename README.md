<div align="center">

# portassh

**Remote SSH/RDP access to Windows machines without admin rights, installation or setup — one exe, run it and connect**

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
and dials a reverse tunnel through your relay server. Then open the **web
dashboard** on the relay: see every online machine, click 🖥 Terminal for a
browser-based shell, 🖥️ RDP for remote desktop, or run a command on several
machines at once.

```sh
ssh -C -i portassh_client.key -o StrictHostKeyChecking=accept-new \
    -o ProxyCommand "ssh -C -i portassh_client.key -W %h:%p -o StrictHostKeyChecking=accept-new support@<relay>" \
    -p 2200 anyuser@localhost
```

Zero prompts: the client key (downloaded from the dashboard) authenticates both
the relay jump and the target. A shared password (baked at build time, shown on
your dashboard) remains as a fallback for machines without the key. Zero config
files on the target, no firewall prompts.

## How it works

```
 your PC / phone                    relay (VPS)                        target Windows
┌─────────┐   ssh -J -C   ┌──────────────────────────┐  reverse tunnel   ┌────────────────────────┐
│ browser ├─────wss──────►│ nginx (basic auth)       │◄────SSH session───┤ portassh.exe           │
│ client  ├───ssh -J -C──►│  sshd :22                │                   │ • sshd  127.0.0.1:2222 │
└─────────┘   direct-    │  hbd  :8898 hb, :8897 api┼───heartbeat───────┤ • cmd.exe via ConPTY   │
             tcpip       │  wsterm 127.0.0.1:8896───┼───ws bridge───────┤ • RDP :3389 forwarded  │
                         │  127.0.0.1:2200+ fwd     │                   └────────────────────────┘
                         └──────────────────────────┘
```

- On the target, the exe runs an **embedded SSH server** (shell / exec / SFTP)
  on loopback only — the firewall never prompts and no admin rights are needed
- In parallel it opens a **reverse tunnel** to the relay using a key baked in
  at build time — no inbound access to the target is required
- The relay runs two tiny helpers: **hbd** (in-memory heartbeat registry, no
  shell forks) and **wsterm** (WebSocket ↔ SSH PTY bridge for the browser)
- All keys and addresses are baked into the exe **obfuscated** at build time
  (XOR with a per-build rolling key — `strings` reveals nothing)

## Features

| | |
|---|---|
| 🖥 **Interactive shell** | cmd.exe / powershell via ConPTY ([go-pty]), full terminal with resize |
| ⚡ **Exec** | the client's raw command line reaches cmd 1:1 — quotes, pipes and backslashes survive; commands run in a persistent shell (no process spawns — antivirus-friendly) |
| 🌐 **Web terminal** | `ssh -s`-style shell in the browser (xterm.js + WebSocket bridge), works from a phone |
| 📊 **Broadcast exec** | select machines on the dashboard, run one command on all — parallel, summary table with exit codes |
| 📦 **Inventory** | installed programs (registry, direct read), antivirus + state, model/CPU/RAM/disks — instant JSON, searchable across the fleet |
| 🖥️ **RDP over SSH** | reverse-forwarded 3389, one-click `.rdp` download from the dashboard |
| 🫥 **System tray** | green/yellow/red status icon, one-click "copy connect command", `-stealth` to hide |
| 🔁 **Resilience** | auto-reconnect (2 s after laptop resume, 5→30 s backoff), 5 s dead-peer detection, ghost-port recycling (stable port per machine), graceful-shutdown offline status |
| 🈳 **UTF-8** | CP866 output is decoded on the fly, no chcp spawning |
| 🖥 **Multi-machine** | registry on the relay, telemetry (user/OS/disk/RAM/version), aliases & notes, up to 100 machines |
| ⏫ **Self-update** | clients check the relay every 30 min or on demand (dashboard button); named builds (ver. 1.15…) |
| 🔐 **Admin mode** | `portassh.exe -admin` — one UAC prompt, separate tunnel, shell running as administrator |
| 📦 **One file** | the 386 build runs on Win10/11 x64 (WOW64), x86 and ARM64 (x86 emulation) |

## Quick start

**1. Relay** — any SSH server (a VPS will do). The tunnel user only needs
TCP forwarding:

```sh
useradd -m -s /bin/sh portassh
mkdir -p /home/portassh/.ssh && chmod 700 /home/portassh/.ssh
```

**2. Keys and build** (requires Go 1.21+, `ssh-keygen` and `python3`):

```sh
./scripts/gen-baked.sh relay.example.com   # ed25519 keys + baked.go
./build.sh                                 # portassh.exe (strings-obfuscated)
```

The script prints a line for the relay's `authorized_keys` — add it once:

```sh
echo "<relay_ed25519.pub>" > /home/portassh/.ssh/authorized_keys
```

**3. Dashboard** — deploy `contrib/sshdash.php` + the `hbd`/`wsterm` helpers
(see the file headers) behind nginx basic auth. It lists online machines and
serves the client key and builds.

**4. Target machine:** download `portassh.zip` from the dashboard, extract and
run `portassh.exe`. Done — the machine appears in the dashboard; connect via
the 🖥 Terminal button or the SSH command.

> Full instructions: [docs/configuration.md](docs/configuration.md)

## Admin access

```sh
portassh.exe -admin        # on the target: one UAC prompt
```

The admin instance keeps its own tunnel (port 2201) and does not interfere
with the regular one. See [docs/configuration.md](docs/configuration.md).

## Multiple machines

Every instance registers itself on the relay:
`PC_NAME port time role user os disk ram version`.
The port is allocated automatically and stays with the machine (ghost-port
recycling after crashes). The dashboard shows aliases, notes, telemetry and
version compliance.

## Security

The trust model is simple and explicit:

- **The exe contains the tunnel private key** — anyone holding the binary can
  open tunnels through your relay. Baked strings are XOR-obfuscated (defeats
  `strings` and static signature scans), but a determined reverse engineer
  can recover them. Never publish built binaries
- **The embedded server accepts exactly the baked client keys** (admin key +
  the shared dashboard client key) and listens on loopback only
- **The relay host key is pinned at build time** — no TOFU, no MITM window
- **The web terminal and inventory** sit behind nginx basic auth; the bridge
  only ever dials loopback
- Vulnerability reports: [SECURITY.md](SECURITY.md)

## Antivirus

An unsigned exe that spawns processes and opens tunnels attracts heuristics
(verified against Kaspersky). portassh minimises the triggers: exec commands
run in **one persistent shell process** (no cmd.exe spawn bursts), diagnostics
use native Win32 API calls instead of utilities, baked strings are
obfuscated, and spawn failures are retried. The complete fix is marking it as
a trusted app in your antivirus or code signing.
Step by step: [docs/troubleshooting.md](docs/troubleshooting.md#forkexec-cmdexe-access-is-denied-intermittent).

## Troubleshooting

Everything that broke during months of live use, with fixes, lives in
[docs/troubleshooting.md](docs/troubleshooting.md) and
[ISSUES.md](ISSUES.md): intermittent `Access is denied`, ghost ports on the
relay, Cyrillic encoding, the 8191 command length limit, relay-side
diagnostics, laptop sleep/resume.

## Repository layout

```
├── main.go                  # core: server, tunnel, exec, telemetry, updates
├── tray_windows.go          # system tray (Shell_NotifyIconW + GDI icons)
├── power_windows.go         # sleep/resume + graceful shutdown (WM_POWERBROADCAST)
├── shellpool_windows.go     # persistent shell pool for exec
├── native_windows.go        # native Win32 diagnostics (sys: prefix)
├── subsys_info_windows.go   # 'info' subsystem: JSON machine passport + inventory
├── oem_windows.go           # CP866→UTF-8 decoding, SysProcAttr.CmdLine, telemetry
├── elevate_windows.go       # -admin: UAC via ShellExecuteW
├── baked.go.example         # configuration template (keys/relay)
├── scripts/gen-baked.sh     # key generation and baked.go writer
├── scripts/obfuscate.py     # baked-string obfuscation (build-time XOR)
├── contrib/sshdash.php      # the web dashboard
├── contrib/hbd/             # relay daemon: heartbeat registry + broadcast exec + inventory proxy
├── contrib/wsterm/          # relay daemon: WebSocket ↔ SSH PTY bridge (web terminal)
├── build.sh                 # build + obfuscation + optional code signing
├── docs/                    # configuration and troubleshooting guides
└── .github/                 # CI, issue/PR templates, dependabot
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
