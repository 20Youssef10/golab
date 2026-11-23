// Package repl implements the Colab Python REPL front end. It is a Go port of
// the interactive and piped behaviour of google-colab-cli's
// src/colab_cli/repl.py and src/colab_cli/commands/execution.py (repl parts).
//
// The package deliberately depends only on a tiny Kernel interface, so the
// runtime implementation can be adapted by internal/app without importing this
// package from a protocol layer. Piped mode (P0) reads blank-line-separated
// blocks from an io.Reader and exits cleanly on EOF; TTY mode (P1) adds raw-mode
// multi-line editing, Python syntax highlighting via chroma, history and
// interrupt handling. Remote output is always sanitised before it reaches the
// terminal, and display data is rendered through a pluggable Renderer.
package repl
