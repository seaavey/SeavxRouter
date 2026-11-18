package backend

import (
	"net/http"
	"time"
)

// NewHTTPClient returns a client backed by one shared, tuned transport.
// Provider engines must use it instead of a bare &http.Client{}: the default
// transport keeps only two idle connections per host, which forces a fresh TCP
// and TLS handshake for every concurrent stream beyond the second.
func NewHTTPClient() *http.Client {
	return &http.Client{Transport: sharedTransport}
}

var sharedTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 200
	t.MaxIdleConnsPerHost = 100
	t.IdleConnTimeout = 90 * time.Second
	t.TLSHandshakeTimeout = 10 * time.Second
	t.ForceAttemptHTTP2 = true
	return t
}()
