package backup

import "testing"

// SetLimitsForTest shrinks the dump part size and the expanded limit, so splitting and the
// over-limit refusal are tested without allocating 256 MiB.
func SetLimitsForTest(t testing.TB, part, limit int64) {
	op, ol := partBytes, expandLimit
	partBytes, expandLimit = part, limit
	t.Cleanup(func() { partBytes, expandLimit = op, ol })
}
