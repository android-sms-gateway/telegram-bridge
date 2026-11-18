package reply

import "github.com/android-sms-gateway/telegram-bridge/internal/bridge"

// OutcomeText exposes the outcome rendering to the external test package, which
// cannot reach the unexported function.
func OutcomeText(outcome bridge.Outcome) string {
	return outcomeText(outcome)
}
