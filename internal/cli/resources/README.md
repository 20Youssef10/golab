# golab

An unofficial Go implementation of the Google Colab CLI, compatible with
`google-colab-cli` 0.8.x command surfaces.

Common commands:

    golab new [-s NAME] [--gpu T4 | --tpu V5E1 | --high-mem]
    golab sessions
    golab status [-s NAME]
    golab exec -f script.py [-s NAME]
    golab run script.py [ARGS...]
    golab upload LOCAL REMOTE / golab download REMOTE LOCAL
    golab install PKGS... [-r REQUIREMENTS]
    golab ssh [-s NAME] [-i KEY] [-- CMD...]
    golab usage / url / log / stop / update

All `[colab] ...` diagnostics are written to stderr; program output is written
to stdout, so commands remain safe to pipe. See `golab --help` for the full list
and `golab <command> --help` for per-command flags.

Authentication uses the same OAuth client or Application Default Credentials
as the Python CLI. `golab whoami` reports the active identity (never tokens).

This project is not affiliated with or endorsed by Google.
