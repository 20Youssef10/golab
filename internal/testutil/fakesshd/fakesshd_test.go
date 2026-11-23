package fakesshd_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/20Youssef10/golab/internal/testutil/fakesshd"
)

func TestUpstreamFailurePresets(t *testing.T) {
	for status := 400; status <= 502; status++ {
		switch status {
		case 400, 401, 403, 404, 429, 502:
		default:
			continue
		}
		f := fakesshd.UpstreamFailure(status)
		if f.Status != status {
			t.Errorf("UpstreamFailure(%d).Status = %d", status, f.Status)
		}
		if f.Body == "" {
			t.Errorf("UpstreamFailure(%d) has no body", status)
		}
	}
	if !strings.Contains(fakesshd.UpstreamFailure(429).Body, `"limit":`) {
		t.Error("429 body must carry a limit")
	}
}

func TestServerLifecycleAndRequestCapture(t *testing.T) {
	s, err := fakesshd.New(fakesshd.Options{Failure: &fakesshd.Failure{Status: 404, Body: "no ssh"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if !strings.HasPrefix(s.URL(), "http://127.0.0.1:") {
		t.Fatalf("URL = %q", s.URL())
	}
	resp, err := http.Get(s.URL() + "/colab/ssh?colab-runtime-proxy-token=T")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("status = %d; want 404", resp.StatusCode)
	}
	r := s.LastRequest()
	if r == nil {
		t.Fatal("no captured request")
	}
	if r.Path != "/colab/ssh" || r.Method != http.MethodGet {
		t.Fatalf("captured = %+v", r)
	}
	if !strings.Contains(r.Query, "colab-runtime-proxy-token=T") {
		t.Fatalf("query = %q", r.Query)
	}
}
