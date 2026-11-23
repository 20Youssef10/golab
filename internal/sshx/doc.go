// Package sshx implements Google Colab's SSH-over-WebSocket transport
// (docs/02 §4) with an embedded x/crypto/ssh client — no system ssh or
// ssh-keygen is required (ADR-005).
//
// The package is transport-only: it takes a runtime proxy URL, a proxy token
// and plain values, and never imports internal/state, internal/cli or
// internal/app. Session resolution/auto-create is the caller's job.
// Modes: interactive shell, `ssh -- CMD` and a raw stdio bridge for external
// OpenSSH/VS Code clients (--proxy-mode).
package sshx
