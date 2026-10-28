package auth

import "time"

// CredentialHealth projects the same eligibility rules used by the engine
// selector. In particular, elapsed cooldown flags are not permanent failures.
// It does not refresh credentials or modify their runtime state.
func CredentialHealth(a *Auth, model string, now time.Time) (string, time.Time) {
	blocked, reason, next := isAuthBlockedForModel(a, model, now)
	if !blocked {
		if a.Status == StatusPending || a.Status == StatusUnknown {
			return "expired", time.Time{}
		}
		return "healthy", time.Time{}
	}
	if reason == blockReasonDisabled {
		return "disabled", time.Time{}
	}
	if !next.IsZero() {
		return "cooldown", next
	}
	return "expired", time.Time{}
}
