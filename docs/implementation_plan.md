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

### Wave 5: Verification, Build & Deployment
- [x] Task 5.1: Build `portassh.exe` (universal 386) and verify SHA-256 in `version.txt`.
- [x] Task 5.2: Build `hbd.linux_amd64` and run all tests.
- [x] Task 5.3: Deploy `authorized_keys`, `hbd`, `sshdash.php`, and `updates/` to `root@5.35.81.198`.
- [x] Task 5.4: Verify live heartbeats, AI Copilot querying, and machine listing.
