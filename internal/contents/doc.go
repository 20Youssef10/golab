// Package contents implements the subset of Jupyter's Contents API used by
// Colab's runtime proxy: listing, reading, chunked writing and deleting files
// on the VM. The upstream implementation this ports is src/colab_cli/contents.py
// from google-colab-cli 0.8.x; the wire contract is documented in docs/02 §3.8.
// All credentials travel as the colab-runtime-proxy-token query parameter; no
// control-plane credential is used here.
package contents
