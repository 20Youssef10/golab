// Package cli is the cobra command layer of golab, the Go port of Google's
// google-colab-cli. It owns flag parsing, the shared session-resolution rule,
// output/exit-code mapping and the orchestration of internal/app's dependency
// graph. It is the only package that imports internal/app; nothing imports it.
//
// Program output goes to stdout and every "[colab] …" diagnostic goes to
// stderr (deliberate deviation Δ1 from upstream, docs/03 §7).
package cli
