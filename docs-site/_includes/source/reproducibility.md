<!-- Generated from docs/USAGE.md §Reproducibility by docs-site/tools/sync-sources.py. Do not edit. -->

### Reproducibility

```bash
golab env freeze -s train -o env.lock.json        # Python, pip freeze, GPU
golab run train.py --lockfile env.lock.json
golab run notebook.ipynb -p EPOCHS=3 --params-file params.json
golab run train.py --bundle --out run.json
golab bundle record --out run.json --file train.py --file data.csv --env KEY=VALUE
golab bundle verify run.json --files-root .
golab bundle verify run.json --env              # re-check in a FRESH VM
```

`env freeze -s NAME -o FILE` records the session's Python version, `pip freeze` output and GPU into a deterministic lockfile. `run --lockfile` verifies and installs it before the script runs. `-p` / `--params-file` set notebook parameters. `bundle record` writes a deterministic manifest of local inputs; `bundle verify` without `--env` checks **recorded metadata only** (and re-hashes local files when `--files-root` is given). `--env` actually allocates a fresh VM and re-checks the environment there — that is the only form that executes anything. No VM is allocated by `record` or metadata-only `verify`.
