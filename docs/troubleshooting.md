# Troubleshooting

Collected from live operation (see CHANGELOG 1.0.0). Verified on a
Mac → VPS → Windows 10 Pro tunnel.

## `fork/exec cmd.exe: Access is denied` (intermittent)

**Cause:** the antivirus throttles process-spawn bursts from an unsigned
binary. It clears itself within 10–30 seconds; portassh retries the spawn
3 times with a 3-second pause.

**Fixes:**
- Add the exe to the antivirus's **trusted applications** with the "do not
  restrict application activity" checkbox — plain "exclusions" do NOT disable
  the behavioural detector
- The definitive fix is code signing (build.sh has an optional signing step:
  `sign.pfx` + `SIGN_PASS`)
- Diagnostics: attempts and the cause are written to `portassh.log`; a final
  failure returns exit code **127**

## The tunnel died and will not come back

1. Check `portassh.log`: a panic stack trace means a bug (please open an
   issue); silence right before the drop probably means the antivirus killed
   the process
2. Reconnect runs in an infinite loop (backoff 5→30 s). If the exe is alive,
   the tunnel comes back within a minute at most
3. Port 2200 showing "refused" for the first ~60 seconds after a drop is the
   ghost of the old sshd session on the relay: the instance hops to the next
   port (2201, 2202, …) and re-registers itself. Check the current port in
   the relay registry (`online.txt`) or in `portassh_connection.txt`
4. Configure `ClientAliveInterval 30` + `ClientAliveCountMax 2` in the
   relay's sshd_config — dead sessions release their ports within a minute

## Cyrillic output shows mojibake

Exec output is decoded from CP866 to UTF-8 automatically. If you still see
mojibake:

- Make sure the client terminal is in UTF-8 (`locale` / terminal settings)
- Utilities that emit UTF-8 themselves (not OEM) will be double-decoded —
  see `oem_windows.go` for the trade-off

## Backslashes in commands

The command line is passed verbatim: a single `\` arrives as `\`. Escape the
way you would in a local cmd session on that machine.

## Command length limit (8191 characters)

That is a cmd.exe argv limitation. Move long scripts and data through files:

```sh
scp -o ProxyJump=root@<relay> -P 2200 script.ps1 anyuser@localhost:
ssh -J root@<relay> -p 2200 anyuser@localhost "powershell -File script.ps1"
```

stdin of the exec channel is wired to the child process, so this also works:

```sh
ssh -J root@<relay> -p 2200 anyuser@localhost "cmd /c more > file" < localfile
```

## A GUI process with a UAC dialog holds the exec channel

`Start-Process -Verb RunAs` without `-Wait` returns control immediately; if a
process still hangs, launch it via `start ""` to detach it from the console.
portassh intentionally has no exec timeout.

## Two machines behind one relay

Every instance takes the next free port: 2200, 2201, … (up to 100). Roles and
ports are visible in the relay registry `online.txt`. To make port 2200 free
within a minute after an unclean machine shutdown, configure
`ClientAliveInterval 30` / `ClientAliveCountMax 2` on the relay.

## Diagnosing from the relay side

```sh
ss -tlnp | grep -E ':22[0-9]{2}'     # forward listeners
ps aux | grep 'sshd: portassh'       # live sessions
tail -20 /home/portassh/online.txt   # machine registry
```
