# Changelog

All notable changes to this project are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [1.28.0] — 2026-10-26

The dashboard becomes the primary control plane; the exe gains deep
diagnostics and machine-friendly behaviour.

### Added
- Web dashboard (`contrib/sshdash.php`): online machine list with telemetry
  (user/OS/disk/RAM/version), aliases & notes, instant search, download
  buttons, force-update button, machine/inventory tabs
- Broadcast exec: run one command on selected machines in parallel, summary
  table with exit codes (executed by the `hbd` relay daemon — no proc_open)
- Web terminal: browser-based shell via the `wsterm` WebSocket ↔ SSH PTY
  bridge (xterm.js, works from a phone)
- Deep inventory: installed programs (direct registry reads), antivirus
  product + state, served as JSON by the `info` subsystem
- RDP over SSH: reverse-forwarded 3389 (target port +10000), one-click `.rdp`
  download
- System tray icon (pure Win32): green/yellow/red status, copy-command menu,
  `-stealth` to hide
- Named builds: `ver. 1.N` counter, clients report their version in the
  heartbeat; the dashboard flags outdated machines
- Fast dead-peer detection: 5 s keepalive timeout (was: minutes of TCP hang)
- Instant reconnect after laptop resume (WM_POWERBROADCAST → close dead
  socket, 2 s reconnect)
- Graceful shutdown: WM_ENDSESSION removes the machine from the dashboard
  immediately
- Ghost-port recycling: a machine reclaims its port after a crash instead of
  drifting to port+1
- Persistent shell pool for exec: no cmd.exe spawn bursts (antivirus-friendly)
- Native Win32 diagnostics over the `sys:` exec prefix (whoami, hostname,
  processes, services, disks — no utilities spawned)
- Force-update signal: dashboard button → clients check within 60 s
- Baked-string obfuscation: build-time XOR (defeats `strings`/static scans)

### Fixed
- exec hang on interactive commands (pool aborts on session close)
- hbd registry rotation (compaction now actually runs at runtime)
- wsterm: go.sum committed; TLS served directly with the full cert chain
- telemetry: DOMAIN\\user kept intact in the registry line
- sysNative: explicit exit status 0

### Changed
- Connect commands carry `-C` (SSH compression on the admin leg; x/crypto
  itself only supports `none`) and `-i portassh_client.key`
- Jump user: dedicated `support@` instead of root
- Heartbeat line extended: user, OS version, free disk, RAM load, version
- exec runs in a persistent shell: cwd/env persist between commands

## [1.0.0] — 2026-10-24

First public release. Battle-tested: ~100 exec commands per day through the
tunnel (PowerShell, cmd, reg, cscript, rundll32, file transfers), verified on
a live Mac → VPS → Windows 10 Pro tunnel.

### Added
- Embedded SSH server on loopback: interactive shell (ConPTY via
  [go-pty](https://github.com/aymanbagabas/go-pty)), command execution, SFTP
- Reverse tunnel to a relay server with baked-in credentials — zero setup on
  the target
- Auto-reconnect with exponential backoff (5→30 s); survives killing sshd on
  the relay (~11 s to recovery)
- Multi-machine: registry on the relay (`online.txt`), automatic free-port
  allocation (2200–2299), up to 100 machines at once
- Admin mode: `portassh.exe -admin` — one UAC prompt, a second instance
  running as administrator with its own tunnel (port 2201)
- 1:1 quoting: the client's raw command line reaches cmd without being
  rebuilt (`SysProcAttr.CmdLine` + `cmd /d /s /c`)
- UTF-8: CP866 output decoded to UTF-8 on the fly, no chcp spawning
- Resilience: spawn retry ×3 when blocked by antivirus (exit 127 on final
  failure), panics isolated with stack traces written to the log
- `HideWindow` for child consoles — no window flashing
- `asInvoker` manifest, icon, file metadata; optional code signing in build.sh
- One universal binary: the 386 build runs on Win10/11 x64 (WOW64),
  x86 and ARM64 (x86 emulation)

### Changed
- Backslashes are passed verbatim: a single `\` arrives as `\`
  (early builds collapsed `\\` into `\` in an unstable way)
- Instance working directory is the user profile, not the desktop

### Security
- The tunnel private key is baked into the binary at build time: whoever
  holds the exe can open tunnels through your relay. Do not publish built
  binaries
- The embedded server accepts exactly one baked client key and listens on
  loopback only
- The relay host key is pinned at build time (no TOFU)
