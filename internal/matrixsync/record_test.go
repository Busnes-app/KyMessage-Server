package matrixsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Busnes-app/ky_server_base/internal/store"
)

func readRecord(t *testing.T, st store.Store) (SweepRecord, bool) {
	t.Helper()
	raw, err := st.Settings().GetSetting(context.Background(), SweepRecordKey)
	if errors.Is(err, store.ErrNotFound) {
		return SweepRecord{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var rec SweepRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		t.Fatalf("record %q: %v", raw, err)
	}
	return rec, true
}

// waitRecord polls until the stored record satisfies ok.
func waitRecord(t *testing.T, st store.Store, ok func(SweepRecord) bool) SweepRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if rec, found := readRecord(t, st); found && ok(rec) {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatal("no matching sweep record")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunRecordsEverySweep(t *testing.T) {
	f := &fakeMAS{failOn: "lock U2"}
	s, st := newSyncer(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go s.Run(ctx, time.Hour, done)
	first := waitRecord(t, st, func(r SweepRecord) bool { return !r.OK })
	if first.Applied != 3 || first.Failed != 1 || first.FailingSince == nil || !strings.Contains(first.Error, "lock bob") {
		t.Fatalf("failed sweep: %+v", first)
	}
	f.mu.Lock()
	f.failOn = ""
	f.mu.Unlock()
	s.Wake()
	second := waitRecord(t, st, func(r SweepRecord) bool { return r.OK })
	if second.Applied != 4 || second.Failed != 0 || second.FailingSince != nil || second.Error != "" {
		t.Fatalf("ok sweep: %+v", second)
	}
	cancel()
	<-done
}

func TestSweepRecordKeepsTheStreakAndBoundsTheError(t *testing.T) {
	ctx := context.Background()
	s, st := newSyncer(t, &fakeMAS{})
	since := s.record(ctx, 0, 1, errors.New(strings.Repeat("é", 400)), nil)
	rec, _ := readRecord(t, st)
	if since == nil || rec.FailingSince == nil || !rec.FailingSince.Equal(*since) || rec.Error == "" ||
		len(rec.Error) > 300 || !utf8.ValidString(rec.Error) {
		t.Fatalf("first failure: %+v", rec)
	}
	if again := s.record(ctx, 0, 1, errors.New("still failing"), since); again != since {
		t.Fatal("a second failure restarted the streak")
	}
	// A restart carries the streak on from the stored record.
	if got := New(&fakeMAS{}, st, "example.com").storedStreak(ctx); got == nil || !got.Equal(*since) {
		t.Fatalf("restart lost the streak: %v", got)
	}
	if s.record(ctx, 4, 0, nil, since) != nil {
		t.Fatal("a success kept the streak")
	}
	if rec, _ := readRecord(t, st); !rec.OK || rec.FailingSince != nil || rec.Error != "" || rec.Applied != 4 {
		t.Fatalf("after success: %+v", rec)
	}
	if New(&fakeMAS{}, st, "example.com").storedStreak(ctx) != nil {
		t.Fatal("an ok record seeded a streak")
	}
}

type brokenSettings struct{ store.SettingsStore }

func (brokenSettings) SetSetting(context.Context, string, string) error {
	return errors.New("disk full")
}

type brokenSettingsStore struct{ store.Store }

func (b brokenSettingsStore) Settings() store.SettingsStore {
	return brokenSettings{b.Store.Settings()}
}

func TestSweepRecordWriteFailureIsOnlyLogged(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	s, st := newSyncer(t, &fakeMAS{})
	s.st = brokenSettingsStore{st}
	if since := s.record(context.Background(), 0, 1, errors.New("boom"), nil); since == nil {
		t.Fatal("a failed write lost the streak")
	}
	if !strings.Contains(logs.String(), "sweep result not recorded: disk full") {
		t.Fatalf("not logged: %q", logs.String())
	}
}
