package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/Busnes-app/ky-primitives/suitedir"

	"github.com/Busnes-app/ky_server_base/internal/kycalendar"
	"github.com/Busnes-app/ky_server_base/internal/matrixinit"
)

// Timing of the KyIdentity lookup, as KyCalendar's KyPost link. Variables so tests can
// shorten them.
var (
	kycalFirstWait = 5 * time.Second
	kycalRefresh   = time.Hour   // a working pair is fetched again this often
	kycalRetry     = time.Minute // first retry after a failure, doubling up to kycalRefresh
	kycalMinGap    = time.Minute // a kick (a refused signature) waits this long after the last lookup
)

// Why there is no KyCalendar pair (kycalView.Reason).
const (
	kycalPending       = "pending"        // not looked up yet
	kycalMatrixOff     = "matrix_off"     // no Matrix stack: never looked up
	kycalNoKyIdentity  = "no_kyidentity"  // suite sign-in is off
	kycalNoPeer        = "no_peer"        // KyIdentity has no KyCalendar paired with KyMessages
	kycalUnauthorized  = "unauthorized"   // KyIdentity refused the console's client credentials
	kycalUnreachable   = "unreachable"    // KyIdentity did not answer usefully
	kycalInvalidPeer   = "invalid_peer"   // KyIdentity's KyCalendar address is not an https origin
	kycalNoCredentials = "no_credentials" // the sign-in client secret could not be read
)

// kycalState is what the last lookup found, for the admin screen.
type kycalState struct {
	mu      sync.Mutex
	reason  string    // "" while paired
	checked time.Time // the last lookup; zero for an environment pair
}

func (k *kycalState) set(reason string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.reason, k.checked = reason, time.Now()
}

func (k *kycalState) get() (string, time.Time) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.reason, k.checked
}

// newKyCalendar pairs the environment's KyCalendar when it names one; otherwise
// StartKyCalendar looks it up.
func (s *Server) newKyCalendar() {
	s.kycalKick = make(chan struct{}, 1)
	s.kycalState.reason = kycalPending
	switch c := s.config.KyCalendar; {
	case !s.config.Matrix.Enabled():
		s.kycalState.reason = kycalMatrixOff
	case c.URL != "":
		s.kycal.Set(&kycalendar.Peer{URL: c.URL, Secret: c.Secret, Source: kycalendar.SourceEnvironment})
		s.kycalState.reason = ""
	}
}

// StartKyCalendar pairs KyCalendar through KyIdentity when the environment names none and
// Matrix is on: it waits up to kycalFirstWait for the first lookup, then keeps the pair
// fresh until ctx ends. cmd/server calls it before serving.
func (s *Server) StartKyCalendar(ctx context.Context) {
	if s.config.KyCalendar.URL != "" || !s.config.Matrix.Enabled() {
		return
	}
	first, cancel := context.WithTimeout(ctx, kycalFirstWait)
	ok := s.resolveKyCalendar(first)
	cancel()
	go s.kycalLoop(ctx, ok)
}

// kickKyCalendar asks for a lookup soon: KyCalendar signed with a secret we do not hold.
func (s *Server) kickKyCalendar() {
	select {
	case s.kycalKick <- struct{}{}:
	default:
	}
}

func (s *Server) kycalLoop(ctx context.Context, ok bool) {
	last, failures := time.Now(), 0
	if !ok {
		failures = 1
	}
	for {
		wait := kycalRefresh
		if failures > 0 {
			wait = min(kycalRetry<<min(failures-1, 16), kycalRefresh)
		}
		next := last.Add(wait)
	sleep:
		for {
			t := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
				break sleep
			case <-s.kycalKick:
				t.Stop()
				// No sooner than kycalMinGap after the last lookup, so refused signatures
				// cannot hammer KyIdentity.
				if soon := last.Add(kycalMinGap); soon.Before(next) {
					next = soon
				}
			}
		}
		last = time.Now()
		if s.resolveKyCalendar(ctx) {
			failures = 0
		} else {
			failures++
		}
	}
}

// resolveKyCalendar asks KyIdentity for KyCalendar and pairs it, reporting whether the lookup
// worked. A refusal unpairs; an unreachable KyIdentity keeps a pair it issued, so a KyIdentity
// restart does not drop meetings. The secret is never logged.
func (s *Server) resolveKyCalendar(ctx context.Context) bool {
	dir, err := s.kyidentity.SuiteDirectory()
	if err != nil {
		return s.unpairKyCalendar(kycalNoCredentials, err)
	}
	if dir == nil {
		return s.unpairKyCalendar(kycalNoKyIdentity, nil)
	}
	peer, err := dir.Peer(ctx, "kycalendar")
	switch {
	case errors.Is(err, suitedir.ErrNoPeer):
		return s.unpairKyCalendar(kycalNoPeer, nil)
	case errors.Is(err, suitedir.ErrUnauthorized):
		return s.unpairKyCalendar(kycalUnauthorized, err)
	case err != nil:
		if s.kycal.Peer() != nil {
			log.Printf("[KYCALENDAR] KyIdentity lookup failed, keeping the current pair: %v", err)
			return false
		}
		return s.unpairKyCalendar(kycalUnreachable, err)
	}
	u, err := matrixinit.Origin(peer.URL)
	if err != nil {
		return s.unpairKyCalendar(kycalInvalidPeer, nil)
	}
	if cur := s.kycal.Peer(); cur == nil || cur.URL != u || string(cur.Secret) != string(peer.Secret) {
		log.Printf("[KYCALENDAR] paired through KyIdentity: %s", u)
	}
	s.kycal.Set(&kycalendar.Peer{URL: u, Secret: peer.Secret, Source: kycalendar.SourceKyIdentity})
	s.kycalState.set("")
	return true
}

// unpairKyCalendar drops the pair for reason, logging once per change of reason.
func (s *Server) unpairKyCalendar(reason string, err error) bool {
	if prev, _ := s.kycalState.get(); prev != reason {
		if err != nil {
			log.Printf("[KYCALENDAR] not paired through KyIdentity (%s): %v", reason, err)
		} else {
			log.Printf("[KYCALENDAR] not paired through KyIdentity (%s)", reason)
		}
	}
	s.kycal.Set(nil)
	s.kycalState.set(reason)
	return false
}

// kycalView is the console's KyCalendar row. It never carries the secret.
type kycalView struct {
	Connected bool       `json:"connected"`
	Source    string     `json:"source,omitempty"` // environment or kyidentity
	URL       string     `json:"url,omitempty"`
	Reason    string     `json:"reason,omitempty"` // why not connected
	CheckedAt *time.Time `json:"checked_at,omitempty"`
}

func (s *Server) handleKyCalendarStatus(w http.ResponseWriter, r *http.Request) {
	var v kycalView
	reason, checked := s.kycalState.get()
	if !checked.IsZero() {
		v.CheckedAt = &checked
	}
	if p := s.kycal.Peer(); p != nil {
		v.Connected, v.Source, v.URL = true, p.Source, p.URL
	} else {
		v.Reason = reason
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, v)
}
