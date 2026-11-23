// Package fakesshd is an in-process fake of Colab's SSH-over-WebSocket endpoint.
//
// It stands up a loopback HTTP server that accepts the real
// GET /colab/ssh upgrade (capturing the exact request target and headers),
// optionally fails the upgrade with the upstream 400/401/403/404/429/502
// statuses and bodies, and otherwise bridges WebSocket frames to a real
// x/crypto/ssh server (ephemeral host key, accept-any client key). It records
// pty-req, window-change, env, exec and shell requests and returns scripted
// exit statuses. The wire contract mirrors docs/02 §4 and the upstream test
// suite tests/test_ssh_wire_contract.py.
package fakesshd
