// Package fakecolab provides an in-process TLS fake of Colab's control plane
// (C1-C8 from docs/02) and Public API. It is ported from the request-mocking
// approach of google-colab-cli's tests/test_client.py and is used by
// internal/colabapi's tests. See fakecolab.go for its exported configuration
// and inspection API.
package fakecolab
