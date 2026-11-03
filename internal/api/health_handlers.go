package api

import (
	"context"
	"net/http"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/appversion"
	"github.com/Busnes-app/ky_server_base/internal/health"
	"github.com/Busnes-app/ky_server_base/internal/synapseadmin"
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
		{Name: "kymessages", Run: func(context.Context) (string, error) { return appversion.String(), nil }},
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
			health.Probe{Name: "synapse-admin", Run: s.synapseAdminProbe},
		)
	}
	if s.rtc != nil {
		probes = append(probes, health.Probe{Name: "livekit", Run: func(ctx context.Context) (string, error) { _, err := s.rtc.Rooms(ctx); return "", err }})
	}
	callsOff := s.mas != nil && s.rtc == nil
	if callsOff {
		// Chat only (KY_MATRIX_CALLS=off): a LiveKit that still answers is left over from calls on.
		probes = append(probes, health.Probe{Name: "livekit", Run: s.leftoverLiveKit})
	}
	components := health.Check(ctx, probeTimeout, probes, m.BackupDBPassword, m.AdminSecret, m.RTCKey, m.RTCSecret)
	if callsOff {
		c := &components[len(components)-1]
		if c.Status == "up" {
			// With calls off nothing evicts its participants: never report it as gone.
			*c = health.Component{Name: "livekit", Status: "down", Error: leftoverLiveKitError}
		} else {
			*c = health.Component{Name: "livekit", Status: health.NotDeployed}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, map[string]any{"matrix": s.mas != nil, "components": components})
}

const leftoverLiveKitError = "still running with calls off, so calls go on unchecked: docker compose rm -sf livekit"

// leftoverLiveKit succeeds when anything answers HTTP at LiveKit's internal origin.
func (s *Server) leftoverLiveKit(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.matrixTargets.LiveKit+"/", nil)
	if err != nil {
		return "", err
	}
	res, err := s.probeHTTP.Do(req)
	if err != nil {
		return "", err
	}
	return "", res.Body.Close()
}

// synapseAdminProbe proves the console can use Synapse's admin API: a console session, one
// admin read, revoked. It has no version.
func (s *Server) synapseAdminProbe(ctx context.Context) (string, error) {
	return "", s.mas.AsConsole(ctx, func(ctx context.Context, tok string) error {
		_, err := s.rooms.Rooms(ctx, tok, synapseadmin.RoomQuery{Limit: 1})
		return err
	})
}
