// Package fakekernel provides an in-process HTTPS + WebSocket fake of Colab's
// runtime proxy and Jupyter kernel, used by internal/runtime's component tests.
//
// It speaks the Jupyter "default" WebSocket protocol (including the binary
// buffer framing described in docs/02 §3.3) and implements the kernel REST
// lifecycle. It is intentionally not a production component: it never touches
// the network outside the test process and never handles real credentials.
package fakekernel
