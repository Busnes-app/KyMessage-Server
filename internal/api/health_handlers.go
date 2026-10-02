package api

import (
	"context"
	"net/http"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/health"
)

const (
	probeTimeout  = 3 * time.Second
	healthTimeout = 5 * time.Second
)

// handleHealth probes every component now, in parallel. Without Matrix: KyMessages and its
// database only.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
	defer cancel()
	probes := []health.Probe{
		{Name: "kymessages", Run: func(context.Context) (string, error) { return config.AppVersion, nil }},
		{Name: "database", Run: func(ctx context.Context) (string, error) { return "", s.store.Ping(ctx) }},
	}
	m := s.config.Matrix
	if s.mas != nil {
		t := s.matrixTargets
		probes = append(probes,
			health.Probe{Name: "synapse", Run: health.Synapse(s.probeHTTP, t.Synapse)},
			health.Probe{Name: "mas", Run: health.MAS(s.probeHTTP, t.MAS, s.mas.Version)},
			health.Probe{Name: "element", Run: health.Element(s.probeHTTP, t.Element)},
			health.Probe{Name: "postgres", Run: health.Postgres(m.DBHost, m.BackupDBPassword)},
		)
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, map[string]any{
		"matrix":     s.mas != nil,
		"components": health.Check(ctx, probeTimeout, probes, m.BackupDBPassword, m.AdminSecret),
	})
}
