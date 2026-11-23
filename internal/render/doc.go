// Package render turns untrusted runtime output into terminal-safe, optionally
// rich text. It implements the rendering half of ADR-010 (glamour markdown,
// HTML→Markdown, Kitty graphics, ANSI passthrough) and the sanitiser required by
// risk R-15 in docs/08: OSC/DCS/APC/PM strings and terminal-query CSI sequences
// are dropped, while SGR colour/attribute sequences are preserved.
//
// Behaviour is derived from upstream src/colab_cli/utils.py (render_display_data,
// print_kitty, handle_image) and the display_output helper in
// src/colab_cli/commands/execution.py. Unlike the Python implementation this
// package never trusts remote text: every unit is sanitised unless the caller
// explicitly opts out with --raw. Rich rendering and Kitty graphics are only
// emitted on a TTY with NO_COLOR unset (ADR-012 §7). TTY detection is delegated
// to internal/platform; this package adds no mutable global state.
//
// Portions derived from google-colab-cli, © Google LLC, Apache-2.0; modified.
package render
