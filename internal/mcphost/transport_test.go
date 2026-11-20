package mcphost

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func emptyResponse() *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}
}

func TestHeaderRTSameOriginOnly(t *testing.T) {
	var seen http.Header
	rt := &headerRT{
		base: rtFunc(func(r *http.Request) (*http.Response, error) {
			seen = r.Header
			return emptyResponse(), nil
		}),
		headers: map[string]string{"Authorization": "Bearer secret"},
		scheme:  "https",
		host:    "example.com",
	}

	req, _ := http.NewRequest(http.MethodGet, "https://example.com/mcp", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got := seen.Get("Authorization"); got != "Bearer secret" {
		t.Fatalf("same-origin header = %q", got)
	}

	req, _ = http.NewRequest(http.MethodGet, "https://evil.example/redirect", nil)
	req.Header.Set("Authorization", "Bearer leaked")
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got := seen.Get("Authorization"); got != "" {
		t.Fatalf("cross-origin header leaked: %q", got)
	}
}

func TestHeaderRTRejectsHTTPSDowngrade(t *testing.T) {
	rt := &headerRT{
		base:    rtFunc(func(*http.Request) (*http.Response, error) { return emptyResponse(), nil }),
		headers: map[string]string{"Authorization": "Bearer secret"},
		scheme:  "https",
		host:    "example.com",
	}
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/mcp", nil)
	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("expected HTTPS-to-HTTP downgrade to be rejected")
	}
}

func TestHeaderClientInvalidEndpoint(t *testing.T) {
	if _, err := headerClient("not a url", nil); err == nil {
		t.Fatal("expected invalid endpoint error")
	}
}
