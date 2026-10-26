# Configuration

All configuration is baked into the exe at build time via `baked.go` — the
target machine receives no settings files at all.

## Quick start

```sh
./scripts/gen-baked.sh relay.example.com   # generates keys and baked.go
./build.sh                                  # builds portassh.exe
```

The script prints the `authorized_keys` line for the relay — add it once.

## baked.go parameters

| Constant | Purpose | Notes |
|---|---|---|
| `defaultRelay` | Relay server address `host[:22]` | Any SSH server with `AllowTcpForwarding` |
| `defaultRelayUser` | SSH user on the relay | The shell can be restricted to `/bin/sh` — only TCP forwards are needed |
| `defaultRPort` | Port on the relay (2200) | If busy, the next one is taken (up to +99) |
| `defaultLPort` | Local port of the embedded SSH server (2222) | 127.0.0.1 only |
| `relayPrivKeyB64` | Private ed25519 tunnel key, base64 | Public part goes to the relay's `authorized_keys` |
| `relayHostKeyLine` | Relay host key in known_hosts format | Pinned at build time |
| `serverHostKeyB64` | Private host key of the embedded SSH server | Shared across builds — clients never see warnings |
| `serverHostPub` | Public host key part (for client known_hosts) | |
| `allowedClientPubKeys` | Client keys the server accepts (slice — e.g. the admin's personal key plus the shared `portassh-client` key) | Public parts of the keys you connect FROM; a slice can't be a `const`, so it lives outside the `const` block in baked.go |

Manual key generation:

```sh
ssh-keygen -t ed25519 -N '' -f relay_ed25519 -C portassh-relay
base64 < relay_ed25519
```

Or use `./scripts/gen-baked.sh` — it generates everything for you.

## Command-line flags

| Flag | Default | Description |
|---|---|---|
| *(none)* | — | Regular mode: tunnel to port 2200, user shell |
| `-admin` | — | Admin instance: UAC prompt, tunnel to port 2201, shell running as administrator |
| `-elevated` | — | Internal marker of an already elevated instance |
| `-relay host[:22]` | from baked.go | A different relay server |
| `-rport N` | 2200 (admin: 2201) | Port on the relay; if busy the next one is taken |
| `-lport N` | 2222 (admin: 2223) | Local port of the embedded SSH server (loopback only) |
| `-shell name` | `cmd.exe` / `$SHELL` | Shell for sessions (e.g. `powershell.exe`) |
| `-keydir path` | `%LOCALAPPDATA%\portassh` | State directory |

Files created next to the exe:

| File | Contents |
|---|---|
| `portassh.log` | Tunnel status, errors, panic stack traces |
| `portassh_connection.txt` | Ready-to-use connect command |
| `portassh_connection_admin.txt` | Connect command of the admin instance |

## Client-side aliases (optional)

```sh
alias pconnect='ssh -o StrictHostKeyChecking=accept-new -J root@<relay> -p 2200 anyuser@localhost'
alias pconnect-admin='ssh -o StrictHostKeyChecking=accept-new -J root@<relay> -p 2201 anyuser@localhost'
```
