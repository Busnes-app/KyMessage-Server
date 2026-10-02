// Package matrixsync keeps MAS in step with KyIdentity's directory as KyMessages records it:
// inactive or unknown users are locked, deleted ones deactivated (never erased), re-enabled
// ones unlocked. KyIdentity's back-channel logout cuts sessions first; this makes it stick.
package matrixsync

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/store"
)

// Kind is a MAS admin action.
type Kind string

const (
	Lock       Kind = "lock"
	Unlock     Kind = "unlock"
	Deactivate Kind = "deactivate"
)

// User is a MAS user with the KyIdentity subject of its upstream link ("" when none).
// Ambiguous: linked to more than one subject.
type User struct {
	ID, Username, Subject          string
	Locked, Deactivated, Ambiguous bool
}

// Action is one change Plan wants in MAS.
type Action struct {
	Kind   Kind
	User   User
	Reason string
}

// Plan compares MAS with dir (subject → "active", "deleted" or another status) and returns
// the actions, failing closed: anything not known to be active is locked.
func Plan(users []User, dir map[string]string) []Action {
	var out []Action
	for _, u := range users {
		if u.Deactivated {
			continue // reactivation restores nothing; never undone here
		}
		if u.Username == ConsoleUsername && u.Subject == "" && !u.Ambiguous {
			continue // the console's service account; linked, it is a person and judged as one
		}
		status, known := dir[u.Subject]
		switch {
		case u.Ambiguous:
			if !u.Locked {
				out = append(out, Action{Lock, u, "linked to several KyIdentity subjects"})
			}
		case u.Subject == "":
			if !u.Locked {
				out = append(out, Action{Lock, u, "no KyIdentity link"})
			}
		case status == "active":
			if u.Locked {
				out = append(out, Action{Unlock, u, "active in KyIdentity"})
			}
		case status == "deleted":
			out = append(out, Action{Deactivate, u, "deleted in KyIdentity"})
		case !u.Locked:
			reason := "not known to KyMessages"
			if known {
				reason = "inactive in KyIdentity"
			}
			out = append(out, Action{Lock, u, reason})
		}
	}
	return out
}

// MAS is the admin surface the syncer needs; *Client implements it.
type MAS interface {
	Users(ctx context.Context) ([]User, error)
	Lock(ctx context.Context, id string) error
	Unlock(ctx context.Context, id string) error
	Deactivate(ctx context.Context, id string) error
}

// Syncer runs Plan against MAS on a timer and whenever Wake is called.
type Syncer struct {
	mas        MAS
	st         store.Store
	serverName string
	wake       chan struct{}
}

func New(mas MAS, st store.Store, serverName string) *Syncer {
	return &Syncer{mas: mas, st: st, serverName: serverName, wake: make(chan struct{}, 1)}
}

// Wake asks for a sweep soon; it never blocks, and wakes during a sweep coalesce into one.
func (s *Syncer) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run sweeps at start, every interval and on Wake. done closes between sweeps, so shutdown
// can wait for it before the store closes. A failure is logged once per streak. Every sweep
// that shutdown did not interrupt is recorded under SweepRecordKey.
func (s *Syncer) Run(ctx context.Context, interval time.Duration, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	failing := false
	since := s.storedStreak(ctx)
	for {
		run, cancel := context.WithTimeout(ctx, 2*time.Minute)
		applied, failed, err := s.sweep(run)
		cancel()
		if ctx.Err() == nil {
			since = s.record(ctx, applied, failed, err, since)
		}
		switch {
		case err != nil && ctx.Err() == nil && !failing:
			log.Printf("[MATRIX] offboarding sweep failing (retrying every %s): %v", interval, err)
			failing = true
		case err == nil && failing:
			log.Printf("[MATRIX] offboarding sweep recovered")
			failing = false
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

// Sweep applies Plan once. Every action is audited, success or failure; one failed action
// does not stop the rest.
func (s *Syncer) Sweep(ctx context.Context) error {
	_, _, err := s.sweep(ctx)
	return err
}

// sweep is Sweep, also counting the MAS actions applied and failed.
func (s *Syncer) sweep(ctx context.Context) (applied, failed int, err error) {
	users, err := s.mas.Users(ctx)
	if err != nil {
		return 0, 0, err
	}
	dir, err := s.st.Users().DirectoryStatuses(ctx, "kyidentity")
	if err != nil {
		return 0, 0, err
	}
	var errs []error
	for _, a := range Plan(users, dir) {
		var err error
		switch a.Kind {
		case Lock:
			err = s.mas.Lock(ctx, a.User.ID)
		case Unlock:
			err = s.mas.Unlock(ctx, a.User.ID)
		case Deactivate:
			err = s.mas.Deactivate(ctx, a.User.ID)
		}
		outcome := "ok"
		if err != nil {
			failed++
			outcome = "error: " + err.Error()
			errs = append(errs, fmt.Errorf("%s %s: %w", a.Kind, a.User.Username, err))
		} else {
			applied++
		}
		// The MAS action happened; record it even if shutdown cancelled the sweep.
		actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		aerr := s.st.Audit().LogAudit(actx, &store.AuditRecord{
			Action:   "matrix." + string(a.Kind),
			Resource: "@" + a.User.Username + ":" + s.serverName,
			Details:  fmt.Sprintf("subject=%q reason=%q outcome=%q", a.User.Subject, a.Reason, outcome),
		})
		acancel()
		if aerr != nil {
			errs = append(errs, aerr)
		}
	}
	return applied, failed, errors.Join(errs...)
}
