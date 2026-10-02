package health

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCheckIsParallelAndBounded(t *testing.T) {
	block := func(ctx context.Context) (string, error) { <-ctx.Done(); return "", ctx.Err() }
	probes := []Probe{{"a", block}, {"b", block}, {"c", block}, {"d", block}, {"e", block}}
	start := time.Now()
	got := Check(context.Background(), 200*time.Millisecond, probes)
	if d := time.Since(start); d > 700*time.Millisecond {
		t.Fatalf("took %s: probes ran in series or ignored the timeout", d)
	}
	for i, c := range got {
		if c.Name != probes[i].Name || c.Status != "down" || !strings.Contains(c.Error, "deadline exceeded") {
			t.Errorf("%+v", c)
		}
	}
}

func TestResultComparesWithPinsAndLinksSource(t *testing.T) {
	pin := Pins["synapse"]
	ok := result("synapse", "v"+pin, nil, nil)
	if ok.Status != "up" || ok.Version != pin || ok.Pinned != pin || ok.Mismatch || ok.Source != "https://github.com/element-hq/synapse/tree/v"+pin {
		t.Errorf("pinned: %+v", ok)
	}
	old := result("synapse", "1.0.0", nil, nil)
	if !old.Mismatch || old.Source != "https://github.com/element-hq/synapse/tree/v1.0.0" {
		t.Errorf("mismatch: %+v", old)
	}
	for name, want := range map[string]string{
		"mas":      "https://github.com/element-hq/matrix-authentication-service/tree/v1.26.0",
		"element":  "https://github.com/element-hq/element-web/tree/v1.26.0",
		"postgres": "https://github.com/postgres/postgres/tree/REL_1_26_0",
	} {
		if got := result(name, "1.26.0", nil, nil).Source; got != want {
			t.Errorf("%s source %q, want %q", name, got, want)
		}
	}
	app := result("kymessages", "0.1.0-dev", nil, nil)
	if app.Version != "0.1.0-dev" || app.Pinned != "" || app.Mismatch || app.Source != "" {
		t.Errorf("app: %+v", app)
	}
}

// An upstream may answer HTML or worse where a version belongs: it never becomes a version
// or a link.
func TestResultRefusesOddVersionText(t *testing.T) {
	for _, v := range []string{"<html><body>", "javascript:alert(1)", strings.Repeat("9", 80)} {
		c := result("element", v, nil, nil)
		if c.Version != "" || c.Source != "" || c.Status != "up" || c.Error != "unrecognised version string" || c.Mismatch {
			t.Errorf("%q: %+v", v, c)
		}
	}
}

func TestResultRedactsSecrets(t *testing.T) {
	c := result("postgres", "", errors.New(`password "hunter2" rejected for hunter2`), []string{"", "hunter2"})
	if c.Status != "down" || strings.Contains(c.Error, "hunter2") || !strings.Contains(c.Error, "[redacted]") {
		t.Errorf("%+v", c)
	}
	m := result("mas", "", errors.New("admin token s3cret refused"), []string{"hunter2", "s3cret"})
	if m.Status != "down" || strings.Contains(m.Error, "s3cret") {
		t.Errorf("mas secret: %+v", m)
	}
	u := result("mas", "", versionUnknown{errors.New("MAS token: HTTP 401")}, nil)
	if u.Status != "up" || u.Error != "version unknown: MAS token: HTTP 401" {
		t.Errorf("version unknown: %+v", u)
	}
}

func serve(t *testing.T, routes map[string]string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if code, loc, redirect := strings.Cut(body, " -> "); redirect {
			w.Header().Set("Location", loc)
			w.WriteHeader(map[string]int{"302": 302, "503": 503}[code])
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestSynapseProbe(t *testing.T) {
	ctx := context.Background()
	hc := NewHTTPClient()
	base := serve(t, map[string]string{"/health": "OK", "/_synapse/admin/v1/server_version": `{"server_version":"1.162.0"}`})
	if v, err := Synapse(hc, base)(ctx); err != nil || v != "1.162.0" {
		t.Errorf("healthy: %q %v", v, err)
	}
	down := serve(t, map[string]string{"/health": "503 -> /"})
	if _, err := Synapse(hc, down)(ctx); err == nil {
		t.Error("a 503 /health passed")
	}
	quiet := serve(t, map[string]string{"/health": "OK"})
	var vu versionUnknown
	if _, err := Synapse(hc, quiet)(ctx); !errors.As(err, &vu) {
		t.Errorf("no server_version: %v", err)
	}
	moved := serve(t, map[string]string{"/health": "302 -> https://elsewhere.example/"})
	if _, err := Synapse(hc, moved)(ctx); err == nil {
		t.Error("a redirect was followed or accepted")
	}
}

func TestMASAndElementProbes(t *testing.T) {
	ctx := context.Background()
	hc := NewHTTPClient()
	called := false
	version := func(context.Context) (string, error) { called = true; return "1.26.0", nil }
	up := serve(t, map[string]string{"/.well-known/openid-configuration": "{}"})
	if v, err := MAS(hc, up, version)(ctx); err != nil || v != "1.26.0" {
		t.Errorf("mas: %q %v", v, err)
	}
	called = false
	if _, err := MAS(hc, serve(t, nil), version)(ctx); err == nil || called {
		t.Errorf("mas without discovery: err=%v version called=%v", err, called)
	}
	el := serve(t, map[string]string{"/version": "1.12.30"})
	if v, err := Element(hc, el)(ctx); err != nil || v != "1.12.30" {
		t.Errorf("element: %q %v", v, err)
	}
}

// Proves only the unreachable-host path; TestResultRedactsSecrets proves redaction.
func TestPostgresProbeUnreachableHostErrorHasNoPassword(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := Postgres("postgres.invalid", "pw-hunter2")(ctx)
	if err == nil || strings.Contains(err.Error(), "pw-hunter2") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckReturnsAtDeadlineWhenProbeIgnoresContext(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	stuck := func(context.Context) (string, error) { <-release; return "1.0.0", nil }
	fine := func(context.Context) (string, error) { return "1.12.30", nil }
	start := time.Now()
	got := Check(context.Background(), 100*time.Millisecond, []Probe{{"mas", stuck}, {"element", fine}})
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("took %s", d)
	}
	if got[0].Status != "down" || !strings.Contains(got[0].Error, "timed out") || got[0].Version != "" {
		t.Errorf("stuck: %+v", got[0])
	}
	if got[1].Status != "up" || got[1].Version != "1.12.30" {
		t.Errorf("fine: %+v", got[1])
	}
}
