package backup_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/capsule"
	"github.com/Busnes-app/ky-primitives/keyfile"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
)

func TestCollectForRunRecordsTheSizeEvenWhenRefused(t *testing.T) {
	backup.SetLimitsForTest(t, 1000, 1<<20)
	cfg, _ := matrixInstance(t, dumpOf(100), dumpOf(5000))
	_, st := sqliteInstance(t)
	s := backup.Settings(context.Background(), st.Settings())
	p, err := backup.CollectForRun(context.Background(), cfg, s, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := backup.LastSize(s)
	if err != nil || !ok || got.Bytes != backup.Measure(p.Files) {
		t.Fatalf("after success: %+v %v %v", got, ok, err)
	}
	backup.SetLimitsForTest(t, 1000, 4096)
	if _, err := backup.CollectForRun(context.Background(), cfg, s, "1.0.0"); err == nil {
		t.Fatal("over the limit accepted")
	}
	if got, _, _ = backup.LastSize(s); got.Bytes != backup.Measure(p.Files) {
		t.Errorf("after refusal: recorded %d, want the measured %d", got.Bytes, backup.Measure(p.Files))
	}
}

func TestLastSizeWarnsFrom75Percent(t *testing.T) {
	_, st := sqliteInstance(t)
	s := backup.Settings(context.Background(), st.Settings())
	if _, ok, err := backup.LastSize(s); ok || err != nil {
		t.Fatalf("fresh: %v %v", ok, err)
	}
	limit := capsule.MaxExpandedBytes
	for _, tc := range []struct {
		bytes   int64
		percent int
		warn    bool
	}{{limit * 3 / 4, 75, true}, {limit*3/4 - 1, 74, false}, {limit, 100, true}} {
		if err := s.Set(backup.LastSizeSetting, strconv.FormatInt(tc.bytes, 10)); err != nil {
			t.Fatal(err)
		}
		got, ok, err := backup.LastSize(s)
		if err != nil || !ok || got.Percent != tc.percent || got.Warning != tc.warn || got.Limit != limit {
			t.Errorf("%d bytes: %+v %v", tc.bytes, got, err)
		}
	}
}

func TestRunMediaNeedsADirAndTheKey(t *testing.T) {
	cfg, _ := payloadConfig(t)
	cfg.Matrix = config.MatrixConfig{ServerName: "example.com", MediaDir: t.TempDir()}
	cfg.Backup.MediaFullKeep = 3
	if _, err := backup.RunMedia(context.Background(), cfg, time.Now()); !errors.Is(err, backup.ErrNoMediaDir) {
		t.Fatalf("no KY_BACKUP_DIR: %v", err)
	}
	cfg.Backup.Dir = t.TempDir()
	if _, err := backup.RunMedia(context.Background(), cfg, time.Now()); err == nil || !strings.Contains(err.Error(), "media key") {
		t.Fatalf("no key yet: %v", err)
	}
	if _, err := keyfile.LoadOrCreate(backup.MediaKeyPath(cfg.Database.DataDir), 32); err != nil {
		t.Fatal(err)
	}
	if res, err := backup.RunMedia(context.Background(), cfg, time.Now()); err != nil || res.Archive == "" {
		t.Fatalf("with key: %+v %v", res, err)
	}
}
