# 10 · Accounts and browser-created sessions

> Audience: operators who use more than one Google account with `golab`, or who
> start runtimes in the Colab Web UI and want to drive them from the CLI.
> Status: design locked by the requester; command surface tracks the
> implementation on `main` and is verified against `golab help` before release.

## 1. Why this exists

`golab` historically held **one** OAuth identity (a keyring entry plus a
`sessions.json` file) and could only attach a browser-created runtime by being
given its exact Public API resource name. The Public API also needs GCP-side
prerequisites (enable `colaboratory.googleapis.com`, set a billing/quota
project) that no CLI flag can create.

This release adds two things:

1. **Named accounts with isolated sessions.** Each Google identity gets its own
   token and its own session store. An existing single-identity installation is
   migrated to the account `default` with no data loss.
2. **Browser-created sessions as first-class sessions.** The CLI discovers
   runtimes created in the Web UI on **either** control plane, lists them, and
   imports them into the active account's local session store.

## 2. Account model

| Concept | Storage | Compatibility |
|---|---|---|
| Account registry | `<config>/accounts.json`, versioned, mode 0600, atomic write | Missing file ⇒ create `default` |
| Default account token | Keyring `google-colab-cli` / `oauth_token`, fallback `<config>/oauth_token.json` | **Unchanged** legacy location |
| Named account token | Keyring `google-colab-cli` / `oauth_token:<name>`, fallback `<config>/oauth_token_<name>.json` mode 0600 | New |
| Default account sessions | `<config>/sessions.json` | **Unchanged** legacy path |
| Named account sessions | Derived, safely-named per-account file in the same config dir | New |

Account names are validated by an allowlist: no path separators, `..`, absolute
paths or reserved device names. Tokens are never printed, logged or placed in a
command line; all errors are redacted.

## 3. Commands

```bash
golab account add NAME          # OAuth flow for NAME; the browser consent is human-only
golab account list              # names, email (when known), active marker; --json supported
golab account show NAME         # non-secret metadata only
golab account use NAME          # set the active account for subsequent invocations
golab account remove NAME       # refuse the active account unless explicitly overridden;
                                # refuse the last remaining account

GOLAB_ACCOUNT=work golab sessions        # one-shot override
golab --account work sessions            # equivalent global flag
```

Every session command (`new`, `sessions`, `status`, `stop`, `use`, `exec`, …)
resolves its store from the active account. With no account configuration at
all, the active account is `default` and behaviour is exactly as before.

**Human-only:** the OAuth consent in `account add`, GCP quota-project setup for
the Public API, and any Drive browser approval are not automated. `account add`
must report that plainly rather than blocking on a TTY that may not exist.

## 4. Browser-created sessions

```bash
golab sessions --web             # list runtimes created in the Web UI (read-only List)
golab attach --all               # import every discovered Web UI runtime not already local
golab attach --runtime runtimes/r-abc --session mynotebook
golab attach --runtime runtimes/r-abc --refresh   # renew a stale proxy token/URL
```

Key properties:

- `sessions --web` and `attach` use the same `List` call on **both** planes. On
  the legacy tunnel plane no GCP setup is required. The Public API path still
  requires the documented GCP prerequisites, which remain human-only.
- `attach` never calls `POST /assign` and never allocates a runtime. It only
  writes a local session record from data already returned by the read-only
  List, so a Web UI runtime keeps running in the browser.
- Runtime proxy tokens expire. `--refresh` re-reads the runtime from the live
  List and updates the local token/URL/expiry without changing the session name
  or any other local state.
- `--all` is idempotent: already-attached runtimes are reported as skipped, not
  duplicated or overwritten. Session names are derived from the runtime id and
  validated against the same rules as `new` (including Windows reserved names).
- If a control plane cannot expose Web UI runtimes, the command says so
  explicitly; it never presents an empty list as success.

## 5. Safety rules

1. Treat a Web UI runtime as someone else's billable resource until attached:
   confirm the runtime id and the active account before `stop`.
2. Never paste a proxy token, cookie or OAuth code into a tool argument or a
   config file. `attach` obtains the token from the authenticated List.
3. Account removal destroys local credentials for that account; it does **not**
   stop or delete any remote runtime. Run `GOLAB_ACCOUNT=<name> golab stop --all`
   first if that is intended.
4. Session names are local to an account. The same name can exist in two
   accounts without collision, and the active account always decides which one
   a command addresses.

## 6. Verification status

Unit and fake-driven tests cover migration from a legacy single-identity
install, account add/list/use/remove/show, per-account session isolation, the
`GOLAB_ACCOUNT` and `--account` overrides, attach on both planes, `--all`
idempotency and `--refresh`. A real OAuth consent, a real browser-created
runtime and GCP quota setup remain human-only and are not claimed as verified.
