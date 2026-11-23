# Golden fixtures (L3) — currently empty

`scripts/golden.sh` runs scenarios in `testdata/golden/<scenario>/` and compares
normalised output with the checked-in fixtures. The harness and the `wire`
secret scan work today; the scenario fixtures do not exist yet.

## Layout

```
testdata/golden/<scenario>/
  cmd       # one shell command per line; $BIN is the CLI under test
  stdout    # expected normalised stdout
  stderr    # expected normalised stderr
  exit      # expected exit code (single integer)
DELTAS.yaml   # intentional Python-vs-Go differences (to be added)
```

## First scenarios to generate (from the Python CLI via L4)

`version`, `help`, `url`, `usage`, `new`, `ls`, `exec`, `run`, `upload`,
`download`, `install`, plus the four log-export formats and the
`SystemExit` exit-code table from upstream `test_run.py`.

## Procedure

1. Generate the baseline with the **Python** CLI through the differential
   proxy (`scripts/differential.sh`), so the fixture is ground truth.
2. `COLAB_BIN=./golab bash scripts/golden.sh --scenario <name> --update`
3. **Review the diff** (mandatory) and record intentional divergences in
   `DELTAS.yaml` (mirrors docs/05's Δ-table). An unlisted diff must fail CI.
4. Normalisation already strips timestamps, UUIDs, 6-hex session names,
   tokens, temp/home paths and CRLF — do not hand-edit those placeholders.

A fixture count and the upstream-test porting matrix live in
`docs/CONFORMANCE.md`.
