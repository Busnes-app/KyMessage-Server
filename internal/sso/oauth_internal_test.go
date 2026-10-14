package sso

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A hung IdP must fail discovery within idpTimeout, even on a context that never ends;
// otherwise every sign-in queues behind the discovery mutex.
func TestDiscoveryAgainstAHungIdPTimesOut(t *testing.T) {
	release := make(chan struct{})
	idp := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release); idp.Close() })
	old := idpTimeout
	idpTimeout = 200 * time.Millisecond
	t.Cleanup(func() { idpTimeout = old })

	flow := newOAuthFlow(idp.URL, "client", "")
	done := make(chan error, 1)
	go func() {
		_, err := flow.authCodeURL(context.Background(), "https://app.example/cb", "s", "v", "n")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("discovery against a hung IdP succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("discovery against a hung IdP did not time out")
	}
}
