// Package runtime is a from-scratch Jupyter kernel client for Colab's runtime
// proxy (docs/02 §3, ADR-008). It implements the kernel REST lifecycle, the
// Jupyter "default" WebSocket protocol (text frames and binary-buffer frames),
// the execution/idle-timeout loop, the output accumulator, stdin and Colab's
// colab_request/colab_reply extension. Control-plane credentials are never
// used here: the only token is the data-plane runtime proxy token.
package runtime
