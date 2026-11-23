// Package colabapi is the Colab control-plane client. It implements the
// C1-C8 tunnel endpoints and the Public API /v1beta/runtimes mapping from
// docs/02, ported from google-colab-cli's src/colab_cli/client.py and
// consumption.py. It accepts an already-authenticated *http.Client and never
// imports internal/state or internal/cli.
package colabapi
