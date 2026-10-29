# Implementation Plan: Fleet Scaling, AI Copilot Optimization & System Hardening

## Overview
Based on an in-depth audit of the `portassh` fleet, backend relay (`hbd`), and dashboard (`sshdash.php`), this plan addresses scalability bottlenecks, AI context bloat, SSRF vulnerabilities, and Windows privileges.

## Waves & Tasks

### Wave 1: Relay Scalability Fix
- [x] Task 1.1: Expand OpenSSH forward permissions in `/home/portassh/.ssh/authorized_keys` on relay server to `permitlisten="127.0.0.1:*",permitopen="127.0.0.1:*"`.
  - *Definition of Done:* Allows machines beyond ports 2200–2209 to bind reverse SSH forwards without `remote listen` rejections.

### Wave 2: AI Copilot Deduplication & SSRF Hardening (`contrib/hbd/ai.go`)
- [x] Task 2.1: Deduplicate `list_machines` tool in `contrib/hbd/ai.go`.
  - Parse `online.txt`, group by `host|role`, retain only the most recent heartbeat within active window (last 5 min, or latest).
  - Format output as structured table with headers (`HOST | PORT | ROLE | USER | OS | DISK | RAM | UPTIME | VER | LAST_SEEN`).
  - *Definition of Done:* AI receives clean 1-line-per-machine summary without 20 duplicate lines of the same host.
- [x] Task 2.2: Harden `webFetch` SSRF protection.
  - Resolve domain using `net.LookupIP(host)` before HTTP dispatch.
  - Verify that all resolved IPs are not Loopback, Private (RFC 1918 / RFC 4193), Link-Local, or Unspecified.
  - *Definition of Done:* Domains pointing to 127.0.0.1 or local IPs (e.g. `*.nip.io`) are strictly rejected.

### Wave 3: Autoruns Diagnostics & Least Privilege SCM (`native_windows.go`, `contrib/sshdash.php`)
- [x] Task 3.1: Explicit Hive/Location prefix in `listAutoruns` (`native_windows.go`).
  - Prefix values with `HKCU\...`, `HKLM\...`, `User Startup\...`, `Common Startup\...`.
  - *Definition of Done:* `sys:autoruns` clearly distinguishes user-level vs machine-wide startup items.
- [x] Task 3.2: Least privilege SCM in `svcControl` (`native_windows.go`).
  - Replace `windows.SC_MANAGER_ALL_ACCESS` with `windows.SC_MANAGER_CONNECT`.
  - Replace `windows.SERVICE_ALL_ACCESS` with `windows.SERVICE_QUERY_STATUS | windows.SERVICE_START | windows.SERVICE_STOP`.
  - *Definition of Done:* Service status and basic control do not fail prematurely with Access Denied under standard accounts.
- [x] Task 3.3: Add `sys:autoruns` and `sys:events` to `$PB_DEFAULTS` in `contrib/sshdash.php`.
  - *Definition of Done:* Quick playbooks include one-click Autoruns and EventLog inspection.

### Wave 4: Dashboard Timeout & Proxy Resilience (`contrib/sshdash.php`)
- [x] Task 4.1: Extend PHP timeouts for AI Copilot endpoints.
  - Set `@set_time_limit(180)` and `'timeout' => 180` for `ai_chat` and `ai_explain_events`.
  - *Definition of Done:* Multi-tool reasoning chains taking >120s do not terminate with 502 Bad Gateway.

### Wave 6: Dashboard Action Methods & Host Matching Fix
- [x] Task 6.1: Fix `$_REQUEST` handling for POST actions in `contrib/sshdash.php` (`setalias`, `addpb`, `delpb`, `force`).
  - *Definition of Done:* Manual machine aliases, playbooks, and force update trigger correctly via `postAct()`.
- [x] Task 6.2: Case-insensitive hostname matching in `allowed-hosts.txt` (`contrib/hbd/main.go`, `contrib/relay/authorized_cmd.sh`).
  - *Definition of Done:* Hostnames written manually in uppercase, lowercase, or mixed case match without rejection.

### Wave 7: High-Performance Networking, Async Streaming & Batching
- [x] Task 7.1: Screen Input Push / Long-Polling (`screen_windows.go`, `contrib/hbd/main.go`).
  - Eliminate 60ms ticker busy-polling in agent. Implement `/input/wait` long-polling with instant wake-up on server-side input event.
  - *Definition of Done:* Agent sends 0 empty requests when idle; response latency on click/key is < 2ms.
- [x] Task 7.2: SSH Connection Multiplexing Pool in `hbd` (`contrib/hbd/main.go`).
  - Maintain pooled `*ssh.Client` per target port with thread-safe access, idle TTL reaper (45s), and auto-reconnect on broken channels.
  - *Definition of Done:* Successive commands on same target reuse existing SSH connection without repeated KEX/Auth handshake.
- [x] Task 7.3: Dynamic Worker Pool for Broadcast Exec (`contrib/hbd/main.go`).
  - Bounded concurrency with worker semaphore (max 32 workers) in `broadcastHandler`.
  - *Definition of Done:* Broadcast across 200+ machines does not exhaust file descriptors or socket buffers.
- [x] Task 7.4: Client Pipeline Diagnostic Execution (`native_windows.go`, `native_other.go`).
  - Add `sys:diag` and `sys:batch` command prefix for multi-command evaluation in 1 RTT.
  - *Definition of Done:* `sys:diag` returns disks, procs, services, and whoami in a single payload.
- [x] Task 7.5: In-Memory Fleet State & `/api/fleet` in `hbd` (`contrib/hbd/main.go`, `contrib/sshdash.php`).
  - `hbd` tracks active fleet state in memory with RWMutex. Expose `/api/fleet` returning structured JSON.
  - *Definition of Done:* Dashboard reads pre-parsed fleet state via fast JSON API without re-parsing `online.txt` on disk.
- [x] Task 7.6: Real-time Server-Sent Events (SSE) Bus (`contrib/hbd/main.go`, `contrib/sshdash.php`).
  - Stream state changes (heartbeat, offline, alerts) via SSE to connected dashboard clients.
  - *Definition of Done:* Dashboard UI updates incrementally via EventSource without full-page DOM polling.


