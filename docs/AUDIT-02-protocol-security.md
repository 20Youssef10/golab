# AUDIT-02 · Protocol + Security Audit (golab vs. upstream `google-colab-cli`)

> **Date:** 2026-11-23 · **Upstream:** `googlecolab/google-colab-cli` HEAD `0abf82d`
> (source at `/tmp/opencode/google-colab-cli`).
> **Method:** static side-by-side reading of every file named in the brief; no
> live Colab traffic was captured and no code was modified. Fakes are **not**
> evidence of live behaviour (AGENTS.md #3).
> **Legend:** ✅ matches upstream/spec · **Δ** deliberate deviation documented in
> docs/05 or docs/08 · ⚠️ accidental mismatch or under-documented assumption ·
> 🔴 unverified by live capture · **HUMAN REQUIRED** = needs a human with a
> Google account, a browser or a billable VM.

---

## 1. Wire-fidelity findings

### 1.1 Control plane (`/tun/m/*`)

| # | Topic | Upstream evidence | Our evidence | Verdict |
|---|---|---|---|---|
| C-1 | XSSI stripping | `client.py:224`, `274-277` strips exact `)]}'\n` | `client.go:34`, `198-210` strips exact prefix on `[]byte` | ✅ |
| C-2 | XSRF GET→POST handshake | `client.py:423-429` (unassign), `431-473` (assign), `505-517` | `client.go:283-319`, `360-418`, `421-432`; `X-Goog-Colab-Token` set at `client.go:172-174` | ✅ |
| C-3 | `authuser=0` on Colab-host requests | `client.py:288-293` — substring host test (`hostname in colab_domain.hostname`) | `client.go:214-243` — added structurally via `colab bool` on tunnel URLs only; no hostname matching at all | ✅ fixes D-7; no substring hazard |
| C-4 | `Accept: application/json` + client agent on every request | `client.py:294-298` (`colab-cli`) | `client.go:170-171` (`github.com/20Youssef10/golab/0.2.0`, `version.ClientAgent()`) | ⚠️ value differs by design — **R-03/DEC-03**; live allow-list probe still **HUMAN REQUIRED** (S-3 identity finding closed) |
| C-5 | `X-Colab-Client-Agent` value | `client.py:39-42` `colab-cli` | `internal/version/version.go:22` `version.ClientAgent()` = `github.com/20Youssef10/golab/<ver>` on every plane (SSH: `sshx/wire.go:30,50`) | ✅ one honest identity (S-3 closed); upstream uses a different value, allow-list still live-unverified |
| C-6 | `nbh` encoding | `client.py:60-64`: UUID with `-`→`_`, pad with `.` to 44 | `types.go:83-103` | ✅ |
| C-7 | `variant`/`accelerator` on `/assign` | `session.py:95-113` always passes `DEFAULT`/`NONE`; `client.py:482-493` emits both | `client.go:268-278`, `321-329` | ✅ |
| C-8 | `shape=hm` only when high-mem is meaningful | `client.py:95-116` (`HIGH_MEM_ONLY_ACCELERATORS = L4,V5E1,V6E1`) | `types.go:176-189` | ✅ |
| C-9 | Assign retry/reconcile | `client.py:442-473`: 3 retries, 502/503/504 + ReadTimeout, 412 fatal, reconcile GET | `client.go:360-418`, `errors.go:49-63` | ✅ (we add overflow guard at `client.go:396-403`) |
| C-10 | Friendly 412 / 400 errors | `client.py:257-272`, `431-473` | `client.go:331-346` typed `ErrTooManyAssignments` / `ErrNoQuota`; `_auth_error_hint` settings flip **not ported** to error text | Δ deliberate: typed errors + settings updates moved to `test-public-api` (`cli/utility_extra.go:220-260`). Wire response handling matches; message text diverges. |
| C-11 | Unassign | `client.py:423-429` GET+POST, path not escaped | `client.go:421-432` GET+POST with `url.PathEscape(endpoint)` | ✅ hardening; endpoint values are UUID-like so no live divergence expected |
| C-12 | `GET /assignments` | `client.py:365-368` | `client.go:442-456` | ✅ |
| C-13 | Public API `GET /v1beta/runtimes` mapping | `client.py:370-421` (shape/variant/accelerator/expiry/name) | `client.go:458-532` | ✅ |
| C-14 | `check_api_access` | `client.py:342-358` persists booleans itself | `client.go:638-645` returns booleans; caller persists in `cli/utility_extra.go:220-260` | ✅ layering Δ (protocol packages never import `state`) |
| C-15 | `GET /ccu-info` | `client.py:336-340`; `consumption.py:20-56` | `client.go:534-552`; `types.go:144-156` | ✅ |
| C-16 | Credentials propagation C7/C8 | `client.py:554-633` here; upstream flow `automation.py:133-232`: GET `dryrun=true`, POST `dryrun=true` w/ `file_id=empty.ipynb`, optional redirect, POST `dryrun=false`; body `<prefix>\n{json}` | `client.go:554-633` (`propagationParams`, `parseFlexibleJSON`, `Confirm` closure) + `cli/automation.go:318-373` | ✅ |
| C-17 | HTTP timeouts | `client.py:300-305` env-driven, unset = none | `client.go:32-35`, `124-155` explicit connect/TLS/header/idle + `60s` total | ✅ fixes D-5 (Δ7) |
| C-18 | Error body in messages | `client.py:317-324`, `306-316` logs full request/response headers + body (redacted by regex) | `client.go:178-193` logs only method/path; body is redacted + capped 2 KiB (`errors.go:65-77`) | ✅ Δ6; see S-6 for redaction-pattern gaps |
| C-19 | Response size cap | upstream none | `client.go:185` `io.LimitReader(...,1<<20)` | Δ defensive; could truncate a very large legitimate error body, not a protocol break |

### 1.2 Kernel REST / WebSocket

| # | Topic | Upstream evidence | Our evidence | Verdict |
|---|---|---|---|---|
| K-1 | REST headers | docs/02 §3.1 + `runtime.py:129-138` | `rest.go:58-69`: `Authorization: Bearer`, `X-Colab-Runtime-Proxy-Token`, `X-Colab-Client-Agent`, `User-Agent: Jupyter Kernel Client`, Accept + Content-Type | ✅ |
| K-2 | WS URL + headers | `runtime.py:105-138` (token in query + headers, no subprotocol) | `ws.go:186-212` (`session_id`, `token` via `url.Values`; `Subprotocols: nil`; same headers) | ✅ |
| K-3 | Connect retry 3× / backoff 2s,4s | `runtime.py:99-177` | `runtime.go:12-17`, `118-142` | ✅ |
| K-4 | Create/inspect/restart/interrupt/delete | `runtime.py` + docs/02 §3.2 | `rest.go:101-165` (`POST /api/kernels` body `{"name":"python3","path":null}`) | ✅ |
| K-5 | Stale kernel id persisted | docs/02 §3.2 ⚠️ upstream only saves when `not self.kernel_id` (`runtime.py:146-151`) | `runtime.go:41-115` + `app/app.go:219-237` always persist effective id via `OnKernelStarted` | ✅ deliberate bug fix; wire requests identical |
| K-6 | WS ping 60s | docs/02 §3.3 | `ws.go:14-18`, `150-167` | ✅ |
| K-7 | Read limit | docs/02 §3.3 | `ws.go:210` 64 MiB; `frame.go:14-17` | ✅ |
| K-8 | Binary framing | docs/02 §3.3 | `frame.go:19-120`: `[u32be n][offsets][json][bufs]`, `nbufs>=2`, offset validation | ✅ |
| K-9 | Execute request | `runtime.py:65-69`, `299` | `kernel.go:65-74`: same content incl. `allow_stdin`, `stop_on_error`, own msg id | ✅ no `kernel_info` handshake (docs/02 §3.4 🔴 item 3) |
| K-10 | Parent filtering + terminal state before filter | `runtime.py:320-326` | `kernel.go:204-213` (`acceptMessage`) | ✅ |
| K-11 | Idle timeout (1s slices, per-message reset) | `runtime.py:24-26`, `301-335` | `kernel.go:12-42`, `83-172` | ✅ (we also handle reply-before-idle at `kernel.go:151-170`) |
| K-12 | Accumulator | `runtime.py:246-259`, docs/02 §3.5 | `accumulator.go:29-84`: no stream merge, `clear_output` ignores `wait`, `update_display_data` replaces in place | ✅ |
| K-13 | Error synthesis when reply status=error but no error output | `runtime.py:262-273` | `kernel.go:178-201` | ✅ |
| K-14 | stdin `input_reply` | `runtime.py:210-238` | `kernel.go:261-294` | ✅ and **Δ** ADR-009.5: our stdin loop is independent, so streaming + stdin works (upstream raises `NotImplementedError`, `runtime.py:195-198`) |
| K-15 | `colab_request` intercept | `runtime.py:53-94`, `automation.py:94-103` | `ws.go:110-148` (hook off reader goroutine), `kernel.go:248-256` (`SendColabReply` with request header as parent), `message.go:80-96` extracts `colab_msg_id`/`authType` | ✅ |
| K-16 | Lost session taxonomy | upstream string-matches `"404"/"401"` (`utils.py:38-47`) | `runtime/errors.go:14-76` typed `ErrSessionLost`, `errors.Is/As` in `cli/helpers.go:242-250` | ✅ deliberate (docs/02 §6) |
| K-17 | Connection-lost text | `runtime.py:304-305` `"Connection was lost."` | `errors.go:24`, `kernel.go:115-118` | ✅ |

### 1.3 Contents API

| # | Topic | Upstream evidence | Our evidence | Verdict |
|---|---|---|---|---|
| F-1 | Token only in query + `authuser=0` | `contents.py:30-52` (`requests.request(..., params=…)` — **no headers**) | `contents.go:291-310`: query token + authuser ✅, **plus extra `X-Colab-Client-Agent` header** at `contents.go:306` | ⚠️ accidental mismatch with docs/02 §3.1 ("query only"). Harmless unless server rejects unknown headers — **HUMAN REQUIRED** (S-1) |
| F-2 | Path encoding | `contents.py:34` `quote(path.strip("/"), safe="/")` | `contents.go:327-337` per-segment `url.PathEscape`, joined with `/` | ✅ |
| F-3 | 404 → not-found | `contents.py:43-44` | `contents.go:316-322` typed `ErrNotFound` | ✅ |
| F-4 | Upload payload | `contents.py:57-73`: one PUT, `chunk:1`, `name=remote.split("/")[-1]`, `path=remote_path` verbatim | `contents.go:256-286`: `chunk:1..N,-1`, `name=path.Base(clean)`, `path=strings.Trim(remotePath,"/")` | ⚠️ **Δ5** streaming is deliberate (D-4), but the `path` field loses a leading `/` relative to upstream and chunking is still 🔴 S6. See S-1/S-2 and §2 item 4. |
| F-5 | Download format handling | `contents.py:75-96` base64/json/text | `contents.go:160-177`, `483-545` | ✅ |
| F-6 | Chunked-upload fallback | n/a | `contents.go:197-246`: on 4xx/501 of the *first* chunk, buffers remainder and single-shot PUTs | ⚠️ only covers explicit rejection; a server that **accepts but ignores** `chunk` would leave a truncated/empty remote file. HIGH — see S-2 |
| F-7 | `edit` aborts on non-404 | upstream swallows every error (`files.py` D-11) | `cli/files.go:269-277` only tolerates `contents.ErrNotFound` | ✅ Δ12 fixes D-11 |

### 1.4 SSH handshake

| # | Topic | Upstream evidence | Our evidence | Verdict |
|---|---|---|---|---|
| H-1 | WS URL | `ssh.py:240-251`: `/colab/ssh?colab-runtime-proxy-token=…&colab-client-agent=colab-cli&colab-client-agent=ssh-mode-<mode>` | `sshx/wire.go:34-56` same shape, agent `github.com/20Youssef10/golab/0.2.0` (`version.ClientAgent()`) unencoded | ✅ model matches, with one honest identity (S-3 closed); token/agent inserted raw in both; live allow-list probe still human-gated |
| H-2 | Pubkey header | `ssh.py:64`, `374-378` `X-Colab-Ssh-Pubkey` verbatim | `sshx/wire.go:22-26`, `82-92` | ✅ |
| H-3 | Modes | `ssh.py:68-70` interactive/proxy/cmd | `sshx/command.go:11-29` + `cli/ssh.go:23-34` | ✅ |
| H-4 | Key discovery/order | `ssh.py:33-37`, `111-153`: `--identity` → `id_ed25519.pub` → `id_ecdsa.pub`, `ssh-keygen -y` fallback; RSA rejected server-side | `sshx/keys.go:30-100`, `167-255`: agent first (Δ ADR-005), then `id_ed25519`/`id_ecdsa`; native pubkey derivation, RSA rejected locally | ✅/Δ2; key *order* differs (agent first) — deliberate |
| H-5 | 400/401/403/404/429/502 messages | `ssh.py:254-357` | `sshx/errors.go:38-122` | ✅ byte-for-byte, including 429 `limit∈[1,5]` |
| H-6 | Binary WS = raw SSH stream, text treated as bytes, close ⇒ EOF | `ssh.py:427-470` | `sshx/wire.go:121-205` (`wsConn.Read` accepts both text and binary; close→EOF) | ✅ |
| H-7 | Remote command | `ssh.py:544-546`, `571-573` | `sshx/command.go:45-54` (`cd /content…; exec ${SHELL:-/bin/bash} -l`; 1 arg = shell line, N args = shellquote) | ✅ |
| H-8 | Host-key policy | `ssh.py:497-517` `StrictHostKeyChecking=no`, null known-hosts | `sshx/session.go:15-29` accept-any callback | ✅ Δ ADR-005; see S-7 |
| H-9 | `--rm` on SIGHUP | `ssh.py:645-706` | `sshx` runner + `cli/ssh.go:105-121` (idempotent teardown, signal handling in runner) | ✅ |
| H-10 | `--force` | upstream has it (`ssh.py:807-817`, `410-424`) | deliberately not ported (COMPAT.md:27) | Δ documented |
| H-11 | Close codes / idle / max frame / ping requirements | not in upstream source; docs/02 §7 item 7 | `ws.go:210` 64 MiB, `wire.go:136` 64 MiB; no SSH-specific ping | 🔴 **HUMAN REQUIRED** |

### 1.5 OAuth

| # | Topic | Upstream evidence | Our evidence | Verdict |
|---|---|---|---|---|
| O-1 | Six scopes | `auth.py:46-54` | `auth/scopes.go:4-23` | ✅ |
| O-2 | Redirect URI | `auth.py:161` | `auth/auth.go:124` | ✅ |
| O-3 | Client config resolution | `auth.py:190-211` (-c → home → bundled) | `auth/oauth2.go:103-151` | ✅ |
| O-4 | Remote flow: prompt/token_usage to stderr, code from stdin | `auth.py:164-187` | `oauth2.go:276-323` | ✅ |
| O-5 | PKCE S256 + `access_type=offline` | `auth.py:172-174` via google-auth-oauthlib defaults (🔴 in docs/02) | `oauth2.go:285-290` explicit S256 + offline | ✅ against documented expectation; 🔴 until captured — §2 item 5 |
| O-6 | Granted scopes persisted / cleared before exchange | `auth.py:181-187` | `oauth2.go:249`, `369-374`, `376-398` | ✅ |
| O-7 | Keyring service/user + 30s timeout | `auth.py:57-59`, `66-89` | `auth/auth.go:125-128` **5s** probe, `tokenstore.go:34-70`, `89-105` | ✅ fixes D-10 (Δ9) |
| O-8 | Legacy token migration | `auth.py:230-236`, `269-276` | `oauth2.go:400-426` | ✅ |
| O-9 | Token payload | upstream `Credentials.to_json()` (access + refresh + client metadata) | `oauth2.go:57-66`, `376-398` stores **access + refresh + client_secret + token URI** | ⚠️ conflict with AGENTS.md "Store the refresh token only" and docs/08 R-09 — see S-4 |
| O-10 | ADC scopes + hint | `auth.py:279-364` | `auth/adc.go:24-32`, `48-111` | ✅ |
| O-11 | Token exchange errors never echo body | upstream logs exceptions (`auth.py:242-243`) | `auth/auth.go:160-189`, `oauth2.go:325-354` explicit no-body errors | ✅ hardening |
| O-12 | OAuth client reuse (R-02) | upstream comment `auth.py:157-160` | same bundled client embedded (`oauth2.go:28-33`, `oauth_config.json`) | ⚠️ unresolved policy risk — see S-5 |

---

## 2. Unresolved 🔴 protocol unknowns (docs/02 §7) — GET-only verification plan

All items below are **HUMAN REQUIRED**. "GET-only" means no allocation POST and no state mutation unless explicitly noted; any SSH/WebSocket attempt consumes a runtime-side SSH slot and must be followed by disconnect, never a POST.

| # | Unknown | Capture (safe probe) | Compare against | What would prove us wrong |
|---|---|---|---|---|
| 1 | Minimal header/query set for kernel REST, kernel WS, Contents (`docs/02 §3.1`, §7.1; 🟡🔴) | Point the binary's injected test HTTP client at a local TLS-terminating **recording proxy**; run `colab ls` / `colab status` (GET-only, no kernel creation) and `colab upload` to a scratch file on an already-live session. Record exact request headers/query. | `rest.go:58-69`, `ws.go:186-200`, `contents.go:291-310`. | Contents server returns 4xx when `X-Colab-Client-Agent` is present → our extra header (F-1) is a real break; or server requires a header we omit. |
| 2 | Real JSON for C1–C8 + error bodies for 400/412/429 (§7.2; 🔴) | `GET /tun/m/assignments`, `GET /tun/m/ccu-info`, `GET /tun/m/assign` (with a fresh nbh — GET does not allocate). Capture XSSI prefix, field names/types. **Do not** send POST /assign unless a human accepts the bill. Error bodies: use an invalid accelerator value on GET /assign and an intentionally expired proxy token on a GET to a data-plane endpoint. | `colabapi/types.go`, `client.go:283-318`, `442-552`, `554-633`. | `variant` returned as string where we decode int (or vice versa), missing `machineShape`, different `unauthorized_redirect_uri` key, or a 429 body not matching `SessionLimit`. |
| 3 | Does `kernel_info_request` improve iopub reliability? (§7.3; 🔴) | On an existing session, attach and issue a harmless `execute_request` with and without a preceding `kernel_info_request`; capture iopub sequence. No VM allocation. | `kernel.go:57-74` (no handshake). | Missing/duplicated `status: idle` without the handshake — our execute loop would hang until `--timeout`. |
| 4 | Chunked Contents upload (§3.8, §7.4; 🔴) | On a live session, upload a ~4 MiB scratch file with `chunk:1,2,…,-1` then `GET /api/contents/<path>?content=1` and compare SHA-256/size with local. Also upload with a single `chunk:1` PUT and compare. Delete scratch file afterwards. | `contents.go:197-246`, `256-286`. | Server writes only the first chunk, or the final empty `chunk:-1` truncates the file → **our streaming upload silently corrupts data** (S-2). This is the highest-priority live check. |
| 5 | PKCE + `access_type` in the OAuth URL (§1.1, §7.5; 🟡🔴) | Run `colab auth` (a human pastes nothing yet) and copy the **authorization URL printed to stderr**; decode its query. No VM needed, no code exchange. | `oauth2.go:285-290`. | Missing `code_challenge`/`code_challenge_method=S256` or `access_type=offline` → our token exchange would be rejected or would not yield a refresh token. |
| 6 | `GET /tun/m/runtime-proxy-token?…&endpoint=<ep>&port=8080` with `X-Colab-Tunnel: Google` (§2.4, §7.6; 🔴) | GET-only against a known endpoint, with the normal bearer token + header. Capture status/body. | We do not implement it; we use `GET /assignments` (C3). | Endpoint works and returns a cheaper/single-assignment response → possible optimisation, not a correctness break. Also confirm adding the header does not affect our existing C3 calls. |
| 7 | SSH handshake close codes, idle timeout, max frame, ping (§7.7; 🔴) | On a live session, open one `/colab/ssh` WS with a valid pubkey, observe upgrade headers, send no data for 5 min, capture any close frame/code and ping frames, then close cleanly. | `sshx/wire.go:61-77`, `121-205` (64 MiB read limit, no SSH-level ping, close→EOF). | Server closes idle connections sooner than our read loop expects, or requires WS pings → our proxy-mode bridge drops silently. |
| 8 | `X-Colab-Client-Agent` allow-listing (§7.8; 🔴 R-03/DEC-03) | Replay a captured GET `/tun/m/assignments` twice with the identical bearer token, once with `colab-cli` and once with `golab/<ver>`; compare status/bodies. Also repeat the SSH upgrade with each agent value. | `client.go:170-171`, `version.go:17-18`, `sshx/wire.go:28-56`. | Non-`colab-cli` value gets 4xx → our honest identity is rejected; per DEC-03 do **not** spoof, escalate to upstream. Also reconcile the two distinct values we currently emit. |

**Cost discipline:** items 1, 2 (GETs only), 5 and 8 require no allocation. Items 3, 4, 6, 7 attach to an already-live session; create at most one, record its endpoint, and unassign before finishing (AGENTS.md live-test rules). End with `colab sessions` showing nothing.

---

## 3. Security audit

Severity is residual risk in the current code, not a claim of exploitability.

### S-1 — Extra header on Contents API requests
**Severity: LOW.** `internal/contents/contents.go:291-310` sends `X-Colab-Client-Agent` in addition to the query-only credential set documented in `docs/02 §3.1` and used by upstream (`contents.py:30-52`).
**Remediation:** either remove the header to match the spec exactly, or keep it and record a GET-only probe (§2 item 1) showing the server tolerates it. Update docs/02 §3.1 accordingly.

### S-2 — Chunked upload can silently truncate a file on a server that accepts but ignores `chunk`
**Severity: HIGH (data loss).** `contents.go:197-246` streams `chunk:1..N` then an empty `chunk:-1`; the single-shot fallback (`contents.go:248-254`) triggers **only** on an explicit 4xx/501 from the first chunk. A server that ignores the `chunk` field would treat each PUT as a full save and the final empty PUT as a whole-file overwrite. docs/02 §3.8/S6 marks this 🔴 and our fakes do not prove live behaviour (AGENTS.md #3).
**Remediation:** **HUMAN REQUIRED** §2 item 4. Until proven, gate streaming behind a verified capability: for files below a threshold use one `chunk:1` PUT (upstream's proven behaviour); for large files, verify remote size via a GET after finalisation and abort/report on mismatch. Add a fake-server test where the server accepts chunk fields but writes each body verbatim.

### S-3 — Client-agent identity: unified on one honest value (CLOSED 2026-11-26)
**Severity: LOW (residual, documented).** The two-identity inconsistency is gone: every plane — control, runtime, contents and SSH — now sends one honest value, `version.ClientAgent()` = `github.com/20Youssef10/golab/0.2.0` (`internal/version/version.go:22`). SSH takes it via `sshx/wire.go:30,50` (`DefaultClientAgent()`) rather than a second hard-coded string. Upstream still uses the single different value `colab-cli` (`client.py:39-42`, `ssh.py:249`), so DEC-03's honesty choice stands.
**Residual / follow-up:** the server-side allow-list is still unknown and cannot be checked without a live probe (§2 item 8, **HUMAN REQUIRED**). If the server rejects our identity, do not spoof — escalate per DEC-03 and document a BYO-agent build option only under `testhooks`. Historical finding retained for traceability.

### S-4 — OAuth token file stores more than the documented "refresh token only"
**Severity: MEDIUM.** `auth/oauth2.go:57-66` and `376-398` persist `access_token`, `refresh_token`, `client_secret`, `token_uri`, and client id. AGENTS.md platform rules state "Store the refresh token only"; docs/08 §3 R-09 mitigation repeats "refresh-token-only storage". Upstream stores `Credentials.to_json()` (`auth.py:252`), so we are at least no worse, but we diverge from our own control. The file is 0600 (`tokenstore.go:188-223`) and keyring tier is preferred (`:89-105`), which limits exposure.
**Remediation:** decide explicitly: (a) stop persisting `access_token` (mint one from the refresh token at startup — one extra HTTP call per process), or (b) amend AGENTS.md/docs/08 to record the deliberate caching of the access token for latency. In either case keep the client secret only when a BYO client requires it; avoid writing it for the bundled public client. Add a golden test that asserts the persisted JSON contains no `access_token` if (a) is chosen.

### S-5 — OAuth client reuse (R-02)
**Severity: MEDIUM (policy/legal, not a code defect).** We embed the same Google cloud-SDK client as upstream (`auth/oauth2.go:28-33`, `oauth_config.json`) with the redirect registered to that client id (`auth.go:124`). This is unavoidable for wire parity but remains an open question in docs/08 §5.
**Remediation (HUMAN REQUIRED):** keep ADC-first and `-c` BYO-client first-class (already present: `auth.go:226-251`, `root.go:110-112`) and ask upstream/Google about third-party reuse. No code change implied; track as a policy item.

### S-6 — Redaction pattern gaps
**Severity: LOW.** `logx.Redact` (`logx/logx.go:107-120`) and `colabapi.redactBody` (`colabapi/errors.go:14-17`, `65-77`) cover the common spellings but not JSON fields `id_token`, generic `secret`, `password`, `assertion`, or `session_token`. Error snippets from the SSH upgrade body are included verbatim before redaction (`sshx/errors.go:68-71`, `85-86`, `118-120`), then passed through `logx.Redact` at `sshx/wire.go:103` and `cli/ssh.go:132-141` — a raw token echoed without a key prefix would survive. History logs full executed code (`cli/helpers.go:213-219`, `history/event.go:161-209`) with only value-level regex redaction, so a hardcoded `password = "…"` inside a script is retained (file is 0600, `history/logger.go:217`).
**Remediation:** extend both regex sets with the missing field names; add a golden test feeding `id_token`/`password`/raw-token bodies through `StatusError.Error()`, `ExplainHandshakeFailure` and `redactedPayload`. Consider scrubbing history `code` payloads behind an opt-in.

### S-7 — SSH host-key accept-any
**Severity: LOW (residual, documented).** `sshx/session.go:19-29` uses `HostKeyCallback: return nil`; upstream disables checking with `StrictHostKeyChecking=no` (`ssh.py:497-517`). The WebSocket channel is TLS-authenticated to Google and carries the bearer/proxy token, so the SSH host key adds little. Documented in ADR-005 / docs/08 §3.
**Remediation:** none required; keep the `#nosec`/`nolint` annotations and ADR reference. Revisit only if Colab ever ships stable host keys (**HUMAN REQUIRED** to verify).

### S-8 — Updater verification: SCT gap (CLOSED 2026-11-24)
**Severity: LOW (resolved).** `update/verify.go:140-175` verifies Fulcio chain + Rekor inclusion proof/SET + observer timestamp + pinned OIDC/workflow identity, and `update/sbom.go:9-51` requires a signed CycloneDX/SPDX attestation. The original audit finding was that Signed Certificate Timestamps were not checked; that gap is **closed**: `internal/update/verify.go:157-163` now adds `verify.WithSignedCertificateTimestamps(1)` in production. The only relaxation is the explicit test-CA opt-out (`RequireSCT=false`), which permits SCT-less bundles but still verifies an SCT when present. Checksum, mandatory signature, mandatory SBOM, downgrade rejection and pinned `api.github.com` (`update/check.go:21-26`, `install.go:14-58`) are all present — D-9 is genuinely fixed.
**Residual risk / follow-up:** none for SCT enforcement. Keep `*.sbom.sigstore.json` publishing (COMPAT.md:24-26) or updates will always be refused, and keep bundles in sigstore v0.3 protobuf format (legacy cosign bundles pass the cosign proof but fail `golab update --install`; see `docs/CONFORMANCE.md` §4). Historical finding retained for traceability; the "documented gap" in docs/08 §3 was corrected the same day.

### S-9 — Dependency / CI supply chain
**Severity: MEDIUM.** `.github/workflows/ci.yml:19-22`, `37-42` uses floating `actions/checkout@v4`, `actions/setup-go@v5` and `govulncheck@latest`; docs/08 R-13 requires "pinned GH Actions by SHA". No `golangci-lint` step (AGENTS.md refine gate) and no fuzz ≥1 h (WP-21 review gate). `go.mod` pulls a large transitive tree (sigstore/TUF/k8s/klog) into a tool that handles Google tokens — justified by the signed-updater requirement but worth an explicit entry in `DEPENDENCIES.md` (AGENTS.md says `docs/DEPENDENCIES.md`, but the file actually lives at the repository root — doc drift worth fixing separately).
**Remediation:** pin all Actions to full commit SHAs; pin `govulncheck` to a version; add `golangci-lint run` to CI; add `docs/DEPENDENCIES.md` one-liners for the new direct dependencies; run the WP-21 review gate. Effort S.

### S-10 — State name validation coverage
**Severity: LOW.** `state.ValidateName` is enforced in `Store.Put` (`state.go:141-145`), `history.NewAt` (`history/logger.go:42-45`), `app.History` (`app.go:194-200`) and every CLI `-s` handler (`cli/root.go:230-239`). `Store.Get`/`Store.Delete` (`state.go:121-139`, `158-173`) do not validate, but all current callers pass names from a validated store or CLI. A future caller could regress D-2.
**Remediation:** call `ValidateName` at the top of `Get`/`Delete` too; add a table test with `../x`, `CON`, empty, 65-char names.

### S-11 — Secret redaction on panic/crash paths
**Severity: LOW.** `logx.Secret` renders `REDACTED` via `String`/`LogValue` (`logx.go:28-45`), and token-bearing errors are wrapped with redacted messages (`runtime/errors.go:86-110`, `colabapi/errors.go:65-77`, `auth/auth.go:174-189`). However, raw token strings are held in plain `string` fields at the point of use (`runtime/rest.go:19,51`, `runtime/ws.go:193`) and could appear in a struct dump on a panic. Upstream's always-on DEBUG file log is replaced by an off-by-default stderr logger (`logx.go:62-71`), which reduces exposure.
**Remediation:** keep `GOTRACEBACK` default; add a test that forces a panic in `restClient.do` with a canary token and asserts the captured stderr contains no canary. Effort S.

### S-12 — Windows file privacy relies on profile ACL
**Severity: LOW (documented Δ).** `platform.MakePrivate` is a no-op on Windows (`platform/platform.go:132-139`); state lives under `%APPDATA%\colab-cli` (`platform.go:29-33`) with `MkdirAll(0700)` (mode ignored on Windows). Same class as upstream D-8, mitigated by profile ACLs.
**Remediation:** keep as documented; add an integration check on Windows that creates the config dir and verifies the ACL excludes `BUILTIN\Users`. **HUMAN REQUIRED** (Windows machine).

**No CRITICAL findings** were identified in this static audit.

---

## 4. Defect verification (docs/01 §10, D-1…D-11)

| ID | Upstream defect | Fix in our code | Evidence | Status |
|---|---|---|---|---|
| D-1 | `_load_raw` swallows errors ⇒ silent state wipe; truncate-in-place | Corrupt file returns `ErrCorrupt`; atomic temp+fsync+link(.bak)+rename under flock | `state/state.go:95-106`, `239-299`; `history/logger.go:194-240` | ✅ Fixed |
| D-2 | Session name path traversal into `history/<name>.jsonl` | `ValidateName` regex + Windows reserved names; enforced on Put and history | `state/state.go:301-320`, `history/logger.go:42-45`, `cli/root.go:230-239` | ✅ Fixed (see S-10 for defensive gap) |
| D-3 | `install` splices package names into Python `'…'` | Packages JSON-marshalled, then `pyRepr`-quoted, decoded with `json.loads` remotely | `cli/automation.go:47-65`; `cli/helpers.go:54-65`; also `cli/automation.go:315-316` (drivemount), `cli/run.go:58-77` (argv) | ✅ Fixed |
| D-4 | Whole-file upload in memory | `WriteStream` 1 MiB chunks + final `chunk:-1` | `contents/contents.go:197-286` | ✅ Fixed structurally; live chunk support 🔴 (S-2 / §2 item 4) |
| D-5 | No default HTTP timeout | Explicit connect/TLS/header/idle + total timeouts in every client | `colabapi/client.go:124-155`, `contents/contents.go:547-587`, `runtime/rest.go:25-38`, `auth/auth.go:275-304`, `sshx/wire.go:58-77`, `update/http.go:15-45` | ✅ Fixed |
| D-6 | Always-on DEBUG file log | Logger off by default; stderr only when opted in; no file sink | `logx/logx.go:47-71` | ✅ Fixed |
| D-7 | Substring hostname check | `authuser=0` added structurally to tunnel URLs; no hostname substring match; no release host override | `client.go:214-243`; `app.go:44-47` (test-only injection); no env/flag host override in `cli/root.go` | ✅ Fixed |
| D-8 | `0600` no-op on Windows | `%APPDATA%` root + `MkdirAll(0700)` + best-effort ACL story; 0600 everywhere else | `platform/platform.go:29-33`, `132-139`; `state.go:86-88`, `275-297` | ✅ mitigation present; Windows ACL verification **HUMAN REQUIRED** (S-12) |
| D-9 | Unverified self-update from user-editable URL | Pinned GitHub API, no config field consulted, mandatory checksum + cosign identity + SBOM + downgrade check | `update/check.go:21-26`, `78-167`; `update/install.go:14-58`; `update/verify.go:94-146`; `update/sbom.go:31-51` | ✅ Fixed (SCT gap S-8) |
| D-10 | Keyring block up to 30 s | 5 s one-shot probe with hard timeout and owner-only file fallback | `auth/auth.go:127`, `221-251`; `auth/tokenstore.go:34-105`, `146-175` | ✅ Fixed |
| D-11 | `edit` clobbers remote file on any download error | Only `ErrNotFound` tolerated; other errors abort before editor opens | `cli/files.go:269-277`, `contents/contents.go:316-322` | ✅ Fixed |

**Regressions found: none** in D-1…D-11. The only re-opened risk is D-4's live-chunk assumption (S-2), which is an unverified external capability, not a code regression.

---

## 5. Implementation plan (test-first)

Ordering follows risk: verify the two wire assumptions that can break users (chunking, agent identity), then close the smaller security gaps.

### P0 — do before any live e2e claim

| ID | Work | Test first (name/new test) | Files | Effort |
|---|---|---|---|---|
| P0-1 | Prove or disprove chunked Contents upload | New `TestWriteStream_ServerIgnoresChunkField` in `internal/contents/contents_test.go`: fake server accepts `chunk:N` but writes each body verbatim; assert final remote content == input (or that client detects mismatch and errors). **HUMAN REQUIRED** live probe §2 item 4 before declaring D-4 done. | `internal/contents/contents.go`, `contents_test.go`, `docs/02 §3.8` | M |
| P0-2 | Unify client agent; verify allow-list | Port upstream `tests/test_client.py` header assertion (if present) plus new `TestClientAgentIdenticalAcrossPlanes` in `internal/app/app_test.go` or `sshx/wire_test.go`; assert one value. **HUMAN REQUIRED** GET replay (§2 item 8). | `internal/version/version.go`, `internal/sshx/wire.go`, `internal/colabapi/client.go`, `docs/08 DEC-03` | S |
| P0-3 | Live GET capture of C1/C3/C6 and error shapes | Add golden fixtures under `testdata/wire/` from a human capture; new comparison test `TestWireFixturesMatchModels` in `internal/colabapi/client_test.go`. | `internal/colabapi/*`, `testdata/wire/`, `docs/02 §2` | M (HUMAN REQUIRED) |

### P1 — close documented security gaps

| ID | Work | Test first | Files | Effort |
|---|---|---|---|---|
| P1-1 | Decide/implement refresh-token-only storage | `TestSavedTokenOmitsAccessToken` in `auth/oauth2_test.go` (or doc amendment test that asserts the documented invariant). | `internal/auth/oauth2.go`, `AGENTS.md`, `docs/08 §3` (doc edit by owner) | S/M |
| P1-2 | Extend redaction patterns | New `TestRedactNoCredentialFields` in `internal/logx/logx_test.go` + `TestStatusErrorRedactsIDToken` in `colabapi/errors_test.go` + `TestExplainHandshakeRedactsRawToken` in `sshx/errors_test.go`. | `internal/logx/logx.go`, `internal/colabapi/errors.go`, `internal/sshx/errors.go` | S |
| P1-3 | Pin CI supply chain | Add `golangci-lint` + SHA-pinned Actions; no test-first needed, verify by reading workflow diff. | `.github/workflows/ci.yml`, `DEPENDENCIES.md` (root; AGENTS.md path is stale) | S |
| P1-4 | Aligned Contents header with spec | Update/rename `TestSendUsesOnlyQueryToken` in `contents_test.go` after deciding F-1. **HUMAN REQUIRED** GET probe (§2 item 1). | `internal/contents/contents.go`, `docs/02 §3.1` | S |
| P1-5 | Live OAuth URL capture | New `TestAuthURLContainsPKCEAndOffline` using a captured URL as golden; **HUMAN REQUIRED** (§2 item 5). | `internal/auth/oauth2.go`, `internal/auth/oauth2_test.go`, `docs/02 §1.1` | S (HUMAN REQUIRED) |

### P2 — hardening / follow-up

| ID | Work | Test first | Files | Effort |
|---|---|---|---|---|
| P2-1 | Validate names in `Store.Get`/`Delete` | Extend `TestValidateName` table in `internal/state/state_test.go`. | `internal/state/state.go` | S |
| P2-2 | Panic-path token leak check | `TestPanicDoesNotLeakToken` capturing stderr with a canary token. | `internal/runtime/rest.go`, new test | S |
| P2-3 | SCT verification / keep documented gap | If adding: `TestVerifyBundleRequiresSCT` in `internal/update/verify_test.go`; otherwise annotate docs/08. | `internal/update/verify.go`, `docs/08 §3` | M |
| P2-4 | REPL sanitizer parity | `TestReplSanitizerDropsNonSGRCSI` in `internal/repl/sanitize_test.go` to align with `render.Sanitize` (`render/sanitize.go:46-60`); decide whether cursor/erase CSI is acceptable. | `internal/repl/sanitize.go`, test | S |
| P2-5 | Live SSH handshake characteristics | Capture and record close codes/idle/ping; new conformance fixture in `internal/sshx/wire_test.go`. **HUMAN REQUIRED** (§2 item 7). | `internal/sshx/wire.go`, `docs/02 §4` | M (HUMAN REQUIRED) |
| P2-6 | Windows ACL verification | **HUMAN REQUIRED** manual check on Windows runner; add step to CI matrix if possible. | `internal/platform/platform.go` | S |

---

## 6. Bottom line

* Wire fidelity is high for the control plane, kernel protocol and SSH status mapping: every upstream request shape we could read is reproduced, with deliberate, documented improvements (typed errors, atomic state, timeouts, honest agent, streaming upload).
* Two things can still break real users and are **not** provable from source or fakes: **(1)** chunked Contents upload support (S-2, HIGH) and **(2)** the server's acceptance of our client-agent identity (S-3, MEDIUM). Both are on the P0 list and require a human.
* No CRITICAL security defects and no regressions of D-1…D-11 were found. The remaining security work is defence-in-depth (redaction coverage, token-storage policy, CI pinning) plus one documented internal inconsistency (two client-agent values).
* Per AGENTS.md #3, **nothing in this report should be read as live proof**; the fakes exercise our inference only. The GET-only plan in §2 is the minimum needed before claiming conformance.
