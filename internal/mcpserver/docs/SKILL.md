# Using golab from an agent

golab is a command-line client for Google Colab runtimes. It is a Go port of
`google-colab-cli`; the command names and flags are mostly interchangeable.

## Safety rules

- Treat every token, OAuth code and runtime-proxy token as secret. Never print
  one, paste one into a notebook, or commit one.
- All allocations create real, billable VMs. Prefer read-only commands
  (`sessions`, `status`, `usage`) and release runtimes with `golab stop` or
  `golab ssh --rm` when finished.
- Validate accelerator names, scripts and flags before allocating.
- Program output is on stdout; `[colab] ...` chatter is on stderr.

## Typical workflow

1. `golab sessions` — see whether a runtime already exists.
2. `golab new -s myvm --gpu T4` — create one when needed.
3. `golab upload ./data.csv content/data.csv` — move files to the VM.
4. `golab exec -s myvm -f script.py` — execute code and stream results.
5. `golab ssh -s myvm -- python script.py` — run a command over SSH.
6. `golab stop -s myvm` — release the VM.

`golab run script.py` performs steps 2–6 in one shot and always tears down
unless `--keep` is given.

## Gotchas

- Remote paths are Linux paths (`/content/...`), even on Windows hosts.
- `install` never splices package names into Python source: the list is sent as
  JSON and decoded in the kernel.
- `auth` and `drivemount` are interactive and need a human for the browser
  consent step; use their fake-server tests for automated verification.
