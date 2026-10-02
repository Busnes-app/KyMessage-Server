package backup

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/Busnes-app/ky-primitives/capsule"
	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/config"
)

// LastSizeSetting holds the last measured expanded size of the server capsule, in bytes.
const LastSizeSetting = "backup_last_expanded_bytes"

// CollectForRun is Collect for a backup run: it records the measured size, over the limit or
// not, for the status screen. A failed write only costs the screen its number.
func CollectForRun(ctx context.Context, cfg *config.Config, s recoveryclient.Settings, appVersion string) (recoveryclient.Payload, error) {
	p, err := Collect(ctx, cfg, appVersion)
	var se *SizeError
	switch {
	case err == nil:
		_ = s.Set(LastSizeSetting, strconv.FormatInt(Measure(p.Files), 10))
	case errors.As(err, &se):
		_ = s.Set(LastSizeSetting, strconv.FormatInt(se.Bytes, 10))
	}
	return p, err
}

// SizeStatus is the last measured size against the capsule's expanded limit.
type SizeStatus struct {
	Bytes   int64 `json:"bytes"`
	Limit   int64 `json:"limit"`
	Percent int   `json:"percent"`
	Warning bool  `json:"warning"` // at or past 75% of Limit
}

// LastSize reads LastSizeSetting; ok is false before the first run.
func LastSize(s recoveryclient.Settings) (SizeStatus, bool, error) {
	v, err := s.Get(LastSizeSetting)
	if errors.Is(err, recoveryclient.ErrNotFound) {
		return SizeStatus{}, false, nil
	}
	if err != nil {
		return SizeStatus{}, false, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return SizeStatus{}, false, fmt.Errorf("backup: %s is %q", LastSizeSetting, v)
	}
	limit := capsule.MaxExpandedBytes
	return SizeStatus{Bytes: n, Limit: limit, Percent: int(n * 100 / limit), Warning: n*4 >= limit*3}, true, nil
}
