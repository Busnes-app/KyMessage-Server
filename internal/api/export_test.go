package api

import (
	"time"

	"github.com/Busnes-app/ky_server_base/internal/health"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// SetRecoveryClientForTest replaces the KyRecovery pairing client. Test-only: this file is not
// part of the package build.
func SetRecoveryClientForTest(s *Server, p recoveryClient) { s.recovery = p }

// SetStoreForTest replaces the handlers' store, so a test can break one write. Test-only.
func SetStoreForTest(s *Server, st store.Store) { s.store = st }

// AttemptsCapForTest is the limiter's hard bound on distinct keys.
const AttemptsCapForTest = attemptsCap

// AttemptKeysForTest returns the client limiter's live keys. Test-only: this file is not part of the
// package build.
func AttemptKeysForTest(s *Server) []string {
	s.clientAttempts.mu.Lock()
	defer s.clientAttempts.mu.Unlock()
	keys := make([]string, 0, len(s.clientAttempts.m))
	for k := range s.clientAttempts.m {
		keys = append(keys, k)
	}
	return keys
}

// AllowAttemptForTest drives the client limiter directly so a test can fill it without paying
// for 10 000 HTTP requests. Test-only.
func AllowAttemptForTest(s *Server, key string, limit int, window time.Duration) bool {
	return s.clientAttempts.allow(key, limit, window)
}

// RegisterDetachedForTest registers one detached handler and returns its unregister func, so a
// test can drive the counter without an HTTP request. Test-only.
func RegisterDetachedForTest(s *Server) func() {
	s.detached.add()
	return s.detached.done
}

// SetHealthTargetsForTest points the Matrix probes at test servers. Test-only.
func SetHealthTargetsForTest(s *Server, t health.Targets) { s.matrixTargets = t }

// DetailOutcomeForTest exposes the audit view's outcome parser. Test-only.
func DetailOutcomeForTest(details string) string { return detailOutcome(details) }
