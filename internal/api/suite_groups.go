package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/Busnes-app/ky-primitives/suitedir"

	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/synapseadmin"
)

// Suite groups (KyIdentity docs/SUITE-DIRECTORY.md, GET /api/suite/groups): each active group
// gets one members-only room, keyed on the group ID, in which nobody but the console is above
// level 0. An archived group's room turns read-only with every managed member removed, and is
// kept. Rooms and rows are never deleted or closed.

// ReconcileSuiteGroups brings every suite group's room in line with KyIdentity's list. A
// failed list changes nothing, and a group missing from it is left as it is.
func (s *Server) ReconcileSuiteGroups(ctx context.Context) error {
	if s.mas == nil || s.meetingRooms == nil {
		return nil
	}
	dir, err := s.kyidentity.SuiteDirectory()
	if err != nil || dir == nil {
		return err
	}
	groups, err := dir.Groups(ctx)
	if err != nil {
		return err
	}
	// Only this loop writes suite_group_rooms, so one read serves the pass.
	rows, err := s.store.Meetings().SuiteGroupRooms(ctx)
	if err != nil {
		return err
	}
	work := groups[:0]
	for _, g := range groups {
		r, ok := rows[g.ID]
		if g.State == suitedir.GroupActive || ok && r.RoomID != "" && r.State != store.SuiteGroupArchived {
			work = append(work, g)
		}
	}
	if len(work) == 0 {
		return nil
	}
	people, err := s.meetingPeople(ctx)
	if err != nil {
		return err
	}
	return s.mas.AsConsole(ctx, func(ctx context.Context, tok string) error {
		var errs []error
		exempt := false
		for _, g := range work {
			var row *store.SuiteGroupRoom
			if r, ok := rows[g.ID]; ok {
				row = &r
			}
			if err := s.reconcileSuiteGroup(ctx, tok, g, row, people, &exempt); err != nil {
				// Synapse's message may name a user: log its route and code only.
				if se := (*synapseadmin.Error)(nil); errors.As(err, &se) {
					err = fmt.Errorf("%s %s: %d %s", se.Method, se.Path, se.Status, se.Errcode)
				}
				errs = append(errs, fmt.Errorf("suite group %s: %w", g.ID, err))
			}
		}
		return errors.Join(errs...)
	})
}

type suiteGroupResult struct {
	outcome          string
	invited, removed int
	refused          int
}

// reconcileSuiteGroup runs under meetingMu, as meeting changes do. Every step is checked
// before it is taken, so a settled group makes no Matrix change and no write.
func (s *Server) reconcileSuiteGroup(ctx context.Context, tok string, g suitedir.Group, row *store.SuiteGroupRoom, people map[string]string, exempt *bool) error {
	s.meetingMu.Lock()
	defer s.meetingMu.Unlock()
	st := s.store.Meetings()
	var res suiteGroupResult
	defer func() {
		if res.outcome != "" {
			s.auditSuiteGroup(ctx, g.ID, row, res)
		}
	}()
	// The console's own rate limits are lifted once a pass, before its first create or invite.
	unthrottle := func() error {
		if *exempt {
			return nil
		}
		*exempt = true
		return s.meetingRooms.ExemptFromRatelimit(ctx, tok, s.consoleMXID())
	}
	if row != nil && row.RoomID != "" {
		blocked, err := s.rooms.Blocked(ctx, tok, row.RoomID)
		if err != nil || blocked { // closed from the console's Rooms page: left alone
			return err
		}
	}
	if g.State != suitedir.GroupActive {
		return s.archiveSuiteGroup(ctx, tok, row, &res)
	}

	var listed []string
	for _, sub := range g.Members {
		if mxid, ok := people[sub]; ok && !slices.Contains(listed, mxid) {
			listed = append(listed, mxid)
		}
	}
	slices.Sort(listed)
	if row == nil {
		// Recorded before the room exists, so a crash after creating it is seen next pass.
		if err := st.CreateSuiteGroupRoom(ctx, g.ID); err != nil {
			return err
		}
		row = &store.SuiteGroupRoom{GroupID: g.ID, State: store.SuiteGroupPending}
	}
	if row.RoomID == "" {
		if err := unthrottle(); err != nil {
			return err
		}
		room, err := s.meetingRooms.CreateMeetingRoom(ctx, tok, g.Name, "")
		if err != nil {
			return err
		}
		row.RoomID, row.State, res.outcome = room, store.SuiteGroupActive, "created"
		// ponytail: a failed write here leaves the room unrecorded and the next pass makes
		// another (listed on the Rooms page). Upgrade path: tag the room with the group ID in
		// state and look it up before creating.
		if err := st.UpdateSuiteGroupRoom(ctx, row); err != nil {
			return err
		}
	} else {
		if row.State == store.SuiteGroupArchived {
			if err := s.meetingRooms.SetReadOnly(ctx, tok, row.RoomID, false); err != nil {
				return err
			}
			res.outcome = "restored"
		}
		room, err := s.rooms.Room(ctx, tok, row.RoomID)
		if err != nil {
			return err
		}
		if room.Name != g.Name {
			if err := s.meetingRooms.SetRoomName(ctx, tok, row.RoomID, g.Name); err != nil {
				return err
			}
			res.outcome = cmp.Or(res.outcome, "updated")
		}
	}

	// Record every invite target before inviting: a lost reply must still leave them removable.
	prev := row.Members
	managed := slices.Clone(prev)
	for _, u := range listed {
		if !slices.Contains(managed, u) {
			managed = append(managed, u)
		}
	}
	slices.Sort(managed)
	if row.State != store.SuiteGroupActive || !slices.Equal(managed, prev) {
		row.Members, row.State = managed, store.SuiteGroupActive
		if err := st.UpdateSuiteGroupRoom(ctx, row); err != nil {
			return err
		}
	}
	current, err := s.meetingRooms.Memberships(ctx, tok, row.RoomID)
	if err != nil {
		return err
	}
	for _, u := range managed {
		if m := current[u]; !slices.Contains(listed, u) && (m == "join" || m == "invite" || m == "knock") {
			if err := s.meetingRooms.Kick(ctx, tok, row.RoomID, u); err != nil {
				return err
			}
			res.removed++
		}
	}
	for _, u := range listed {
		// Never invited, or newly managed. Someone who left or declined is not re-invited
		// until they drop out of the group and come back; a ban is never overridden.
		if m := current[u]; m == "" || m == "leave" && !slices.Contains(prev, u) {
			if err := unthrottle(); err != nil {
				return err
			}
			err := s.meetingRooms.Invite(ctx, tok, row.RoomID, u)
			var se *synapseadmin.Error
			if errors.As(err, &se) && se.Status == http.StatusForbidden {
				res.refused++
				continue
			}
			if err != nil {
				return err
			}
			res.invited++
		}
	}
	if res.invited+res.removed+res.refused > 0 {
		res.outcome = cmp.Or(res.outcome, "updated")
	}
	if !slices.Equal(listed, row.Members) {
		row.Members = listed
		return st.UpdateSuiteGroupRoom(ctx, row)
	}
	return nil
}

// archiveSuiteGroup makes a group's room read-only and removes every member the console
// manages there. The row turns archived last, so a failure half way repeats next pass.
func (s *Server) archiveSuiteGroup(ctx context.Context, tok string, row *store.SuiteGroupRoom, res *suiteGroupResult) error {
	if row == nil || row.RoomID == "" || row.State == store.SuiteGroupArchived {
		return nil
	}
	if err := s.meetingRooms.SetReadOnly(ctx, tok, row.RoomID, true); err != nil {
		return err
	}
	current, err := s.meetingRooms.Memberships(ctx, tok, row.RoomID)
	if err != nil {
		return err
	}
	for _, u := range row.Members {
		if m := current[u]; m == "join" || m == "invite" || m == "knock" {
			if err := s.meetingRooms.Kick(ctx, tok, row.RoomID, u); err != nil {
				return err
			}
			res.removed++
		}
	}
	row.Members, row.State, res.outcome = nil, store.SuiteGroupArchived, "archived"
	return s.store.Meetings().UpdateSuiteGroupRoom(ctx, row)
}

// auditSuiteGroup records a change by IDs and counts only: no group name or member list.
func (s *Server) auditSuiteGroup(ctx context.Context, groupID string, row *store.SuiteGroupRoom, res suiteGroupResult) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), roomAuditTimeout)
	defer cancel()
	room := ""
	if row != nil {
		room = row.RoomID
	}
	s.audit(ctx, "", &http.Request{}, "suite_group.reconcile", groupID, auditFields(res.outcome,
		"room", room, "invited", strconv.Itoa(res.invited), "removed", strconv.Itoa(res.removed), "refused", strconv.Itoa(res.refused)))
}
