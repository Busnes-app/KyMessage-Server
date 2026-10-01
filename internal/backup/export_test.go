package backup

import "testing"

// SetMessagesBudgets lowers the messages capsule's part and total budgets so split and
// over-limit tests prove the rules with megabytes instead of hundreds of MiB.
func SetMessagesBudgets(t *testing.T, part, total int64) {
	oldPart, oldTotal := messagesPartBudget, messagesTotalBudget
	messagesPartBudget, messagesTotalBudget = part, total
	t.Cleanup(func() { messagesPartBudget, messagesTotalBudget = oldPart, oldTotal })
}

// MaxEventParts is the import's part ceiling, which collection and the drill must share.
const MaxEventParts = maxEventParts
