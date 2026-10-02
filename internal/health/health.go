// Package health probes each component of a KyMessages deployment once, in parallel, and
// compares running versions with the ones docker-compose.matrix.yml pins.
package health

//go:generate go run ./genpins -compose ../../docker-compose.matrix.yml -out pins.go

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Component is one probe's result. Error is probe text for the admin screen; secrets passed
// to Check are redacted from it.
type Component struct {
	Name     string `json:"name"`
	Status   string `json:"status"` // "up" or "down"
	Version  string `json:"version"`
	Pinned   string `json:"pinned"`
	Mismatch bool   `json:"mismatch"`
	Source   string `json:"source"`
	Error    string `json:"error"`
}

// Probe checks one component and returns its running version ("" when it has none).
type Probe struct {
	Name string
	Run  func(ctx context.Context) (string, error)
}

// Targets are the Matrix services' internal origins.
type Targets struct{ Synapse, MAS, Element string }

// ComposeTargets are the origins docker-compose.matrix.yml gives the app on its default network.
var ComposeTargets = Targets{Synapse: "http://synapse:8008", MAS: "http://mas:8080", Element: "http://element:8080"}

// versionUnknown marks a component that answered but would not say its version: it is up.
type versionUnknown struct{ err error }

func (v versionUnknown) Error() string { return "version unknown: " + v.err.Error() }

// Check runs every probe in parallel, each under its own timeout, and returns results in
// probe order. It returns by the deadline even when a probe ignores its context; that
// probe is reported down and its late answer is discarded.
func Check(ctx context.Context, timeout time.Duration, probes []Probe, secrets ...string) []Component {
	out := make([]Component, len(probes))
	var wg sync.WaitGroup
	for i, p := range probes {
		wg.Go(func() {
			pctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			type answer struct {
				v   string
				err error
			}
			ch := make(chan answer, 1) // buffered: a probe that outlives its deadline still exits
			go func() {
				v, err := p.Run(pctx)
				ch <- answer{v, err}
			}()
			select {
			case a := <-ch:
				out[i] = result(p.Name, a.v, a.err, secrets)
			case <-pctx.Done():
				out[i] = result(p.Name, "", fmt.Errorf("timed out: %w", pctx.Err()), secrets)
			}
		})
	}
	wg.Wait()
	return out
}

var (
	versionText = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,63}$`)
	release     = regexp.MustCompile(`^[0-9]+(\.[0-9]+)+$`)
)

func result(name, version string, err error, secrets []string) Component {
	c := Component{Name: name, Status: "up", Pinned: Pins[name]}
	if err != nil {
		var vu versionUnknown
		if !errors.As(err, &vu) {
			c.Status = "down"
		}
		c.Error = clip(redact(err.Error(), secrets))
	}
	switch version = strings.TrimPrefix(strings.TrimSpace(version), "v"); {
	case version == "":
	case versionText.MatchString(version):
		c.Version = version
	case c.Error == "":
		c.Error = "unrecognised version string"
	}
	c.Mismatch = c.Pinned != "" && c.Version != "" && c.Version != c.Pinned
	c.Source = source(name, c.Version)
	return c
}

// source links a running version to its exact upstream source tree.
func source(name, version string) string {
	if !release.MatchString(version) {
		return ""
	}
	switch name {
	case "synapse":
		return "https://github.com/element-hq/synapse/tree/v" + version
	case "mas":
		return "https://github.com/element-hq/matrix-authentication-service/tree/v" + version
	case "element":
		return "https://github.com/element-hq/element-web/tree/v" + version
	case "postgres":
		return "https://github.com/postgres/postgres/tree/REL_" + strings.ReplaceAll(version, ".", "_")
	}
	return ""
}

func redact(s string, secrets []string) string {
	for _, x := range secrets {
		if x != "" {
			s = strings.ReplaceAll(s, x, "[redacted]")
		}
	}
	return s
}

func clip(s string) string {
	if len(s) <= 300 {
		return s
	}
	return strings.ToValidUTF8(s[:300], "") + "…"
}

// NewHTTPClient is the probes' client: no proxy from the environment, no redirects.
func NewHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// fetch GETs u and returns at most 64 KiB of its body when the answer is 200.
func fetch(ctx context.Context, hc *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", req.URL.Path, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<10))
}

// Synapse: /health must answer OK; the version comes from the unauthenticated
// /_synapse/admin/v1/server_version, which the client listener serves.
func Synapse(hc *http.Client, base string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		body, err := fetch(ctx, hc, base+"/health")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(string(body)) != "OK" {
			return "", errors.New("/health did not answer OK")
		}
		var v struct {
			ServerVersion string `json:"server_version"`
		}
		body, err = fetch(ctx, hc, base+"/_synapse/admin/v1/server_version")
		if err == nil {
			err = json.Unmarshal(body, &v)
		}
		if err != nil {
			return "", versionUnknown{err}
		}
		return v.ServerVersion, nil
	}
}

// MAS: public discovery for liveness (no listener serves MAS's health resource), then the
// version from the admin API.
func MAS(hc *http.Client, base string, version func(context.Context) (string, error)) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if _, err := fetch(ctx, hc, base+"/.well-known/openid-configuration"); err != nil {
			return "", err
		}
		v, err := version(ctx)
		if err != nil {
			return "", versionUnknown{err}
		}
		return v, nil
	}
}

// Element serves its version as the plain-text file /version.
func Element(hc *http.Client, base string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		body, err := fetch(ctx, hc, base+"/version")
		return string(body), err
	}
}

// Postgres connects to the synapse database as the read-only kybackup role.
func Postgres(host, password string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		u := url.URL{Scheme: "postgres", User: url.UserPassword("kybackup", password),
			Host: net.JoinHostPort(host, "5432"), Path: "/synapse", RawQuery: "connect_timeout=3"}
		db, err := sql.Open("pgx", u.String())
		if err != nil {
			return "", errors.New("postgres: bad connection settings") // the error may quote the DSN
		}
		defer db.Close()
		var v string
		err = db.QueryRowContext(ctx, "SHOW server_version").Scan(&v)
		return v, err
	}
}
