// Package nb is a minimal, dependency-free implementation of the parts of
// nbformat v4.5 that the Colab CLI needs: notebooks, code/markdown cells,
// outputs, attachments and ordered access to code cells with their
// "# @title" labels (docs/05 §5). Unknown JSON fields are preserved across a
// parse/marshal round-trip. Upstream source: colab_cli/converter.py and the
// nbformat dependency it uses (google-colab-cli HEAD).
package nb
