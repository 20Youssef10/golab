// Package history writes and reads the per-session JSONL event log stored at
// <configdir>/history/<name>.jsonl, and exports it to ipynb/md/txt/jsonl.
// It is the Go counterpart of upstream colab_cli/history.py and
// colab_cli/converter.py (google-colab-cli HEAD). Writes use the same
// cross-process flock + temp + fsync + rename atomicity as internal/state,
// and session names are validated with state.ValidateName (fixes D-2).
//
// Payloads are free-form JSON objects flattened into each log line to keep
// the Python on-disk format. The Logger constructor is per-session so the
// Append/Read/Path API needs no session argument; a package-level
// ListSessions enumerates the history directory. Context-aware variants
// AppendContext/ReadContext are provided for callers that own a context.
package history
