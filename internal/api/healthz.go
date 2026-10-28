package api

import (
	"context"
	"net/http"

	suitehealth "github.com/Busnes-app/ky-primitives/health"
	"github.com/Busnes-app/ky-primitives/logging"

	"github.com/Busnes-app/ky_server_base/internal/health"
)

// suiteHealth builds the public /healthz handler: the database, and with Matrix the
// Synapse, MAS and LiveKit upstreams. Liveness only; the admin Health page keeps versions.
func (s *Server) suiteHealth() http.Handler {
	lg, err := logging.New(logging.Config{App: "kymessages"})
	if err != nil {
		panic(err) // fixed App name; cannot fail
	}
	checks := []suitehealth.Check{{Name: "database", Run: s.store.Ping}}
	if s.mas != nil {
		t := s.matrixTargets
		// MAS discovery only: a public route must not drive admin API calls.
		noVersion := func(context.Context) (string, error) { return "", nil }
		checks = append(checks,
			suitehealth.Check{Name: "synapse", Timeout: probeTimeout, Run: health.Live(health.Synapse(s.probeHTTP, t.Synapse))},
			suitehealth.Check{Name: "mas", Run: health.Live(health.MAS(s.probeHTTP, t.MAS, noVersion))},
		)
	}
	if s.rtc != nil {
		checks = append(checks, suitehealth.Check{Name: "livekit", Run: func(ctx context.Context) error { _, err := s.rtc.Rooms(ctx); return err }})
	}
	return suitehealth.Handler("kymessages", lg, checks...)
}
