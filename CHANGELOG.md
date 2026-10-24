# Changelog

All notable changes to this project are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

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
