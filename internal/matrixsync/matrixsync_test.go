package matrixsync

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

func TestPlan(t *testing.T) {
	u := func(id, sub string, locked, deact bool) User {
		return User{ID: id, Username: id, Subject: sub, Locked: locked, Deactivated: deact}
	}
	dir := map[string]string{"a": "active", "i": "inactive", "d": "deleted"}
	got := Plan([]User{
		u("active-open", "a", false, false),                                                         // nothing
		u("active-locked", "a", true, false),                                                        // unlock
		u("inactive-open", "i", false, false),                                                       // lock
		u("inactive-locked", "i", true, false),                                                      // nothing
		u("deleted-open", "d", false, false),                                                        // deactivate
		u("deleted-locked", "d", true, false),                                                       // deactivate
		u("unknown-open", "x", false, false),                                                        // lock
		u("nolink-open", "", false, false),                                                          // lock
		u("nolink-locked", "", true, false),                                                         // nothing
		u("gone", "d", true, true),                                                                  // never touched again
		u("revived", "a", true, true),                                                               // deactivated stays deactivated
		{ID: "multi-open", Username: "multi-open", Subject: "a", Ambiguous: true},                   // lock, though "a" is active
		{ID: "multi-locked", Username: "multi-locked", Subject: "a", Ambiguous: true, Locked: true}, // nothing
	}, dir)
	want := []Action{
		{Unlock, u("active-locked", "a", true, false), "active in KyIdentity"},
		{Lock, u("inactive-open", "i", false, false), "inactive in KyIdentity"},
		{Deactivate, u("deleted-open", "d", false, false), "deleted in KyIdentity"},
		{Deactivate, u("deleted-locked", "d", true, false), "deleted in KyIdentity"},
		{Lock, u("unknown-open", "x", false, false), "not known to KyMessages"},
		{Lock, u("nolink-open", "", false, false), "no KyIdentity link"},
		{Lock, User{ID: "multi-open", Username: "multi-open", Subject: "a", Ambiguous: true}, "linked to several KyIdentity subjects"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

type fakeMAS struct {
	mu     sync.Mutex
	users  []User
	calls  []string
	failOn string // "kind id" that errors
	listed chan struct{}
	after  func() // runs after each action
}

func (f *fakeMAS) Users(context.Context) ([]User, error) {
	if f.listed != nil {
		f.listed <- struct{}{}
	}
	return f.users, nil
}

func (f *fakeMAS) do(kind Kind, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := string(kind) + " " + id
	f.calls = append(f.calls, c)
	if f.after != nil {
		f.after()
	}
	if c == f.failOn {
		return errors.New("boom")
	}
	return nil
}

func (f *fakeMAS) Lock(_ context.Context, id string) error       { return f.do(Lock, id) }
func (f *fakeMAS) Unlock(_ context.Context, id string) error     { return f.do(Unlock, id) }
func (f *fakeMAS) Deactivate(_ context.Context, id string) error { return f.do(Deactivate, id) }

func newSyncer(t *testing.T, f *fakeMAS) (*Syncer, store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	mk := func(sub, status string) *store.User {
		u := &store.User{ID: "usr_" + sub, Username: sub, Role: "user", Status: status, SSOProvider: "kyidentity", SSOSubject: sub}
		if _, err := st.Users().CreateDirectoryUser(ctx, u, store.DirectoryEvent{ID: "c-" + sub, Revision: 1}); err != nil {
			t.Fatal(err)
		}
		return u
	}
	mk("sub-a", "active")
	mk("sub-i", "inactive")
	d := mk("sub-d", "active")
	if _, err := st.Users().DeleteDirectoryUser(ctx, d, store.DirectoryEvent{ID: "d-sub-d", Revision: 2}); err != nil {
		t.Fatal(err)
	}
	f.users = []User{
		{ID: "U1", Username: "alice", Subject: "sub-a", Locked: true},
		{ID: "U2", Username: "bob", Subject: "sub-i"},
		{ID: "U3", Username: "carol", Subject: "sub-d"},
		{ID: "U4", Username: "dave"},
	}
	return New(f, st, "example.com"), st
}

func auditRows(t *testing.T, st store.Store) []*store.AuditRecord {
	t.Helper()
	rows, _, err := st.Audit().ListAuditRecords(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []*store.AuditRecord
	for _, r := range rows {
		if strings.HasPrefix(r.Action, "matrix.") {
			out = append(out, r)
		}
	}
	return out
}

func TestSweepAppliesPlanAndAudits(t *testing.T) {
	f := &fakeMAS{}
	s, st := newSyncer(t, f)
	if err := s.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"unlock U1", "lock U2", "deactivate U3", "lock U4"}; !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls %v", f.calls)
	}
	rows := auditRows(t, st)
	if len(rows) != 4 {
		t.Fatalf("audit rows = %d", len(rows))
	}
	got := map[string][2]string{}
	for _, r := range rows {
		got[r.Resource] = [2]string{r.Action, r.Details}
	}
	want := map[string][2]string{
		"@alice:example.com": {"matrix.unlock", `subject="sub-a" reason="active in KyIdentity" outcome="ok"`},
		"@bob:example.com":   {"matrix.lock", `subject="sub-i" reason="inactive in KyIdentity" outcome="ok"`},
		"@carol:example.com": {"matrix.deactivate", `subject="sub-d" reason="deleted in KyIdentity" outcome="ok"`},
		"@dave:example.com":  {"matrix.lock", `subject="" reason="no KyIdentity link" outcome="ok"`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("audit rows\n got %v\nwant %v", got, want)
	}
}

// Shutdown stops the sweep: no MAS action starts after its context ends.
func TestSweepStopsActingAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeMAS{after: cancel}
	s, st := newSyncer(t, f)
	if err := s.Sweep(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want context.Canceled", err)
	}
	if len(f.calls) != 1 || len(auditRows(t, st)) != 1 {
		t.Fatalf("calls %v, audit rows %d after cancel", f.calls, len(auditRows(t, st)))
	}
}

func TestSweepAuditsFailuresAndContinues(t *testing.T) {
	f := &fakeMAS{failOn: "lock U2"}
	s, st := newSyncer(t, f)
	if err := s.Sweep(context.Background()); err == nil {
		t.Fatal("want error")
	}
	if want := []string{"unlock U1", "lock U2", "deactivate U3", "lock U4"}; !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls %v", f.calls)
	}
	failed := 0
	for _, r := range auditRows(t, st) {
		if strings.Contains(r.Details, `outcome="error: boom"`) {
			failed++
			if r.Resource != "@bob:example.com" {
				t.Fatalf("resource %q", r.Resource)
			}
		}
	}
	if failed != 1 {
		t.Fatalf("failed rows = %d", failed)
	}
}

// A MAS action that completed is audited even when shutdown cancels the sweep under it.
func TestSweepAuditsActionsAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeMAS{after: cancel}
	s, st := newSyncer(t, f)
	_ = s.Sweep(ctx)
	if got := len(auditRows(t, st)); got != len(f.calls) || got == 0 {
		t.Fatalf("audit rows = %d for %d MAS calls", got, len(f.calls))
	}
}

func TestWakeNeverBlocks(t *testing.T) {
	s := New(&fakeMAS{}, nil, "example.com")
	start := time.Now()
	for range 1000 {
		s.Wake()
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("Wake blocked")
	}
}

func TestRunSweepsAtStartOnWakeAndClosesDone(t *testing.T) {
	f := &fakeMAS{listed: make(chan struct{}, 10)}
	s, _ := newSyncer(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go s.Run(ctx, time.Hour, done)
	wait := func(what string) {
		select {
		case <-f.listed:
		case <-time.After(5 * time.Second):
			t.Fatalf("no sweep %s", what)
		}
	}
	wait("at start")
	s.Wake()
	wait("on wake")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("done not closed")
	}
}
