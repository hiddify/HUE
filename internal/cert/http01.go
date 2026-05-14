package cert

import (
	"net/http"
	"strings"
	"sync"
)

// HTTP01Challenger implements lego's challenge.Provider for HTTP-01
// and also exposes an http.Handler that the HUE listener mounts at
// /.well-known/acme-challenge/.
//
// Lifecycle:
//   1. RequestACME calls Present(domain, token, keyAuth) before
//      driving lego.Client.Certificate.Obtain.
//   2. Let's Encrypt's validator hits HUE at
//      http://<domain>/.well-known/acme-challenge/<token>.
//   3. Our handler looks up keyAuth by token and returns it verbatim.
//   4. After validation completes, lego calls CleanUp(domain, token,
//      keyAuth) — we drop the entry.
//
// Concurrency: a single Challenger instance is shared across the
// process. Multiple in-flight cert requests are safe.
type HTTP01Challenger struct {
	mu     sync.RWMutex
	tokens map[string]string // token → keyAuth
}

func NewHTTP01Challenger() *HTTP01Challenger {
	return &HTTP01Challenger{tokens: make(map[string]string)}
}

// Present is the lego challenge.Provider hook.
func (c *HTTP01Challenger) Present(domain, token, keyAuth string) error {
	c.mu.Lock()
	c.tokens[token] = keyAuth
	c.mu.Unlock()
	return nil
}

// CleanUp drops the token after validation completes (success or fail).
func (c *HTTP01Challenger) CleanUp(domain, token, keyAuth string) error {
	c.mu.Lock()
	delete(c.tokens, token)
	c.mu.Unlock()
	return nil
}

// Handler serves /.well-known/acme-challenge/<token>. Mount this on
// the HUE listener — see hue.go.
func (c *HTTP01Challenger) Handler() http.Handler {
	const prefix = "/.well-known/acme-challenge/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		token := strings.TrimPrefix(r.URL.Path, prefix)
		c.mu.RLock()
		keyAuth, ok := c.tokens[token]
		c.mu.RUnlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(keyAuth))
	})
}
