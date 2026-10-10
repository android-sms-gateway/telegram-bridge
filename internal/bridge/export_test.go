package bridge

// FormatForwardMessageChunksForTest exposes the chunking helper for tests.
func FormatForwardMessageChunksForTest(phoneNumber, message string) ([]string, error) {
	return formatForwardMessageChunks(phoneNumber, message)
}

// TextLengthForTest exposes the Telegram UTF-16 length helper for tests.
func TextLengthForTest(s string) int {
	return textLength(s)
}

// MessageBudgetForTest exposes the per-message character budget for tests.
func MessageBudgetForTest(phoneNumber string) int {
	return messageBudget(phoneNumber)
}
