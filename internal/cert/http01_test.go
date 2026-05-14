package cert

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTP01Challenger_PresentServeCleanUp(t *testing.T) {
	t.Parallel()
	c := NewHTTP01Challenger()
	if err := c.Present("example.com", "tok-1", "keyauth-payload"); err != nil {
		t.Fatalf("Present: %v", err)
	}

	srv := httptest.NewServer(c.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/.well-known/acme-challenge/tok-1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "keyauth-payload" {
		t.Fatalf("body = %q, want %q", string(body), "keyauth-payload")
	}

	if err := c.CleanUp("example.com", "tok-1", "keyauth-payload"); err != nil {
		t.Fatalf("CleanUp: %v", err)
	}
	resp2, err := http.Get(srv.URL + "/.well-known/acme-challenge/tok-1")
	if err != nil {
		t.Fatalf("GET after cleanup: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("after cleanup status = %d, want 404", resp2.StatusCode)
	}
}

func TestHTTP01Challenger_UnknownToken(t *testing.T) {
	t.Parallel()
	c := NewHTTP01Challenger()
	srv := httptest.NewServer(c.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/.well-known/acme-challenge/missing")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHTTP01Challenger_WrongPrefix(t *testing.T) {
	t.Parallel()
	c := NewHTTP01Challenger()
	_ = c.Present("example.com", "tok-1", "k")
	srv := httptest.NewServer(c.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/not-acme/tok-1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
