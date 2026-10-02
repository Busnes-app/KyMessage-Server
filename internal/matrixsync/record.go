package matrixsync

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"
)

// SweepRecordKey is the setting the syncer alone writes after each sweep; the console reads it.
const SweepRecordKey = "matrix_sweep_last"

// sweepErrorMax bounds the recorded error. MAS client errors carry method, path and status,
// never a token or a response body (mas.go).
const sweepErrorMax = 300

// SweepRecord is the last sweep's outcome. FailingSince is the start of the current failing
// streak, nil when the sweep succeeded.
type SweepRecord struct {
	FinishedAt   time.Time  `json:"finished_at"`
	OK           bool       `json:"ok"`
	Error        string     `json:"error"`
	Applied      int        `json:"applied"`
	Failed       int        `json:"failed"`
	FailingSince *time.Time `json:"failing_since"`
}

// record writes one sweep's outcome and returns the failing streak's start for the next one.
// A failed write is logged: the record is for the console; the sweep already happened.
func (s *Syncer) record(ctx context.Context, applied, failed int, err error, since *time.Time) *time.Time {
	now := time.Now().UTC()
	rec := SweepRecord{FinishedAt: now, OK: err == nil, Applied: applied, Failed: failed}
	if err == nil {
		since = nil
	} else {
		if since == nil {
			since = &now
		}
		rec.Error, rec.FailingSince = clipError(err.Error()), since
	}
	b, merr := json.Marshal(rec)
	if merr != nil {
		return since
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if werr := s.st.Settings().SetSetting(wctx, SweepRecordKey, string(b)); werr != nil {
		log.Printf("[MATRIX] sweep result not recorded: %v", werr)
	}
	return since
}

// storedStreak is the failing streak a previous process recorded, so a restart keeps its start.
func (s *Syncer) storedStreak(ctx context.Context) *time.Time {
	raw, err := s.st.Settings().GetSetting(ctx, SweepRecordKey)
	var rec SweepRecord
	if err != nil || json.Unmarshal([]byte(raw), &rec) != nil || rec.OK {
		return nil
	}
	return rec.FailingSince
}

func clipError(v string) string {
	if len(v) <= sweepErrorMax {
		return v
	}
	return strings.ToValidUTF8(v[:sweepErrorMax], "")
}
