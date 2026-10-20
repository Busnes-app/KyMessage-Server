// Package matrixsync keeps MAS in step with KyIdentity's directory as KyMessages records it:
// inactive or unknown users are locked and their sessions ended, deleted ones deactivated (never
// erased), re-enabled ones unlocked. A lock alone leaves sessions working; ending them is what cuts.
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
	// EndSessions finishes every MAS session of a person who is not active, also once locked,
	// so a session a failed or earlier sweep left behind is still ended.
	EndSessions Kind = "session_end"
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
// the actions, failing closed: anything not known to be active is locked and its sessions ended
// (an agent only locked: re-enabling its owner restores it). agents maps a
// registered agent's MAS user ID to its owner's subject; an unlinked agent follows its owner,
// but is only ever locked, never deactivated: deleting an agent is an admin's call.
func Plan(users []User, dir map[string]string, agents map[string]string) []Action {
	var out []Action
	for _, u := range users {
		if u.Deactivated {
			continue // reactivation restores nothing; never undone here
		}
		if (u.Username == ConsoleUsername || u.Username == NotesUsername) && u.Subject == "" && !u.Ambiguous {
			continue // a service account; linked, it is a person and judged as one
		}
		if owner, ok := agents[u.ID]; ok && u.Subject == "" && !u.Ambiguous {
			switch status, known := dir[owner]; {
			case status == "active":
				if u.Locked {
					out = append(out, Action{Unlock, u, "agent owner active in KyIdentity"})
				}
			case !u.Locked:
				reason := "agent owner not known to KyMessages"
				if known {
					reason = "agent owner " + status + " in KyIdentity"
				}
				out = append(out, Action{Lock, u, reason})
			}
			continue
		}
		status, known := dir[u.Subject]
		var reason string
		switch {
		case u.Ambiguous:
			reason = "linked to several KyIdentity subjects"
		case u.Subject == "":
			reason = "no KyIdentity link"
		case status == "active":
			if u.Locked {
				out = append(out, Action{Unlock, u, "active in KyIdentity"})
			}
			continue
		case status == "deleted":
			out = append(out, Action{Deactivate, u, "deleted in KyIdentity"})
			continue
		case known:
			reason = "inactive in KyIdentity"
		default:
			reason = "not known to KyMessages"
		}
		if !u.Locked {
			out = append(out, Action{Lock, u, reason})
		}
		out = append(out, Action{EndSessions, u, reason})
	}
	return out
}

// MAS is the admin surface the syncer needs; *Client implements it.
type MAS interface {
	Users(ctx context.Context) ([]User, error)
	Lock(ctx context.Context, id string) error
	Unlock(ctx context.Context, id string) error
	Deactivate(ctx context.Context, id string) error
	Sessions(ctx context.Context, userID string) ([]Session, error)
	FinishSession(ctx context.Context, kind SessionKind, id string) (already bool, err error)
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

// Sweep applies Plan once. Every action is audited, success or failure (an EndSessions per
// session it finds); one failed action does not stop the rest.
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
	registered, err := s.st.Agents().ListAgents(ctx)
	if err != nil {
		return 0, 0, err
	}
	agents := make(map[string]string, len(registered))
	for _, a := range registered {
		agents[a.MASUserID] = a.OwnerSubject
	}
	var errs []error
	for _, a := range Plan(users, dir, agents) {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if a.Kind == EndSessions {
			n, f, err := s.endSessions(ctx, a)
			applied, failed = applied+n, failed+f
			if err != nil {
				errs = append(errs, err)
			}
			continue
		}
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
		if aerr := s.audit(ctx, a, fmt.Sprintf("outcome=%q", outcome)); aerr != nil {
			errs = append(errs, aerr)
		}
	}
	return applied, failed, errors.Join(errs...)
}

// endSessions finishes every active MAS session of a.User: browser sessions first, then OAuth
// 2.0 (with their refresh tokens) and compatibility sessions. MAS then has Synapse delete each
// session's device, which Synapse checks on every request, ahead of its 2-minute cache of MAS's
// token introspection. Each session is audited as matrix.session_end; none found, none written.
func (s *Syncer) endSessions(ctx context.Context, a Action) (applied, failed int, err error) {
	sessions, err := s.mas.Sessions(ctx, a.User.ID)
	if err != nil {
		err = fmt.Errorf("%s %s: %w", a.Kind, a.User.Username, err)
		return 0, 1, errors.Join(err, s.audit(ctx, a, fmt.Sprintf("outcome=%q", "error: "+err.Error())))
	}
	var errs []error
	for _, ss := range sessions {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		outcome := "ended"
		already, ferr := s.mas.FinishSession(ctx, ss.Kind, ss.ID)
		switch {
		case ferr != nil:
			failed++
			outcome = "error: " + ferr.Error()
			errs = append(errs, fmt.Errorf("%s %s %s %s: %w", a.Kind, a.User.Username, ss.Kind, ss.ID, ferr))
		case already:
			outcome = "already_ended"
		default:
			applied++
		}
		if aerr := s.audit(ctx, a, fmt.Sprintf("session=%q kind=%q outcome=%q", ss.ID, ss.Kind, outcome)); aerr != nil {
			errs = append(errs, aerr)
		}
	}
	return applied, failed, errors.Join(errs...)
}

// audit records a as matrix.<kind> with fields after its subject and reason. The MAS action
// happened; it is recorded even if shutdown cancelled the sweep.
func (s *Syncer) audit(ctx context.Context, a Action, fields string) error {
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return s.st.Audit().LogAudit(actx, &store.AuditRecord{
		Action:   "matrix." + string(a.Kind),
		Resource: "@" + a.User.Username + ":" + s.serverName,
		Details:  fmt.Sprintf("subject=%q reason=%q %s", a.User.Subject, a.Reason, fields),
	})
}
