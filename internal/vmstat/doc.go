// Package vmstat builds the kernel-side snippets used by golab's VM inspection
// commands (ps, tail, grep, du, find, fetch). It takes plain values and emits
// source strings; it never imports internal/state or internal/cli, so it stays
// independently testable and promotable.
//
// Security contract: every user-controlled value is embedded as a JSON literal
// that the snippet decodes with json.loads (the same trick install uses), then
// shell-quoted with shlex.quote before any command line is assembled. No user
// string is ever spliced into generated Python source. Each snippet prints a
// delimited JSON envelope so the CLI can extract structured output even when
// IPython emits warnings around it.
package vmstat
