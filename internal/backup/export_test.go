package backup

import "testing"

// SetMessagesBudgets lowers the messages capsule's part and total budgets so split and
// over-limit tests prove the rules with megabytes instead of hundreds of MiB.
func SetMessagesBudgets(t *testing.T, part, total int64) {
	oldPart, oldTotal := messagesPartBudget, messagesTotalBudget
	messagesPartBudget, messagesTotalBudget = part, total
	t.Cleanup(func() { messagesPartBudget, messagesTotalBudget = oldPart, oldTotal })
}

// SetMessagesFileCap lowers the per-member file cap so a part overflowing it needs no 64 MiB.
func SetMessagesFileCap(t *testing.T, limit int64) {
	old := messagesFileCap
	messagesFileCap = limit
	t.Cleanup(func() { messagesFileCap = old })
}

// MaxEventParts is the import's part ceiling, which collection and the drill must share.
const MaxEventParts = maxEventParts
