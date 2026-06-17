// Copyright (c) 2026 Arie Radle. MIT License.

package controller

import (
	"time"

	shekelv1alpha1 "github.com/arieradle/shekel-controller/api/v1alpha1"
)

// computePeriodEnd returns the time at which the current period should reset.
// Returns the zero time for PeriodNone (no automatic reset).
func computePeriodEnd(period shekelv1alpha1.PeriodType, start time.Time) time.Time {
	utc := start.UTC()
	y, m, d := utc.Date()

	switch period {
	case shekelv1alpha1.PeriodDaily:
		return time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
	case shekelv1alpha1.PeriodMonthly:
		return time.Date(y, m+1, 1, 0, 0, 0, 0, time.UTC)
	case shekelv1alpha1.PeriodRolling24h:
		return start.Add(24 * time.Hour)
	default: // PeriodNone or unknown
		return time.Time{}
	}
}

// periodResetDue returns true if the current period has elapsed and a reset
// should be performed. Always returns false for PeriodNone.
func periodResetDue(period shekelv1alpha1.PeriodType, start time.Time, now time.Time) bool {
	if period == shekelv1alpha1.PeriodNone {
		return false
	}
	end := computePeriodEnd(period, start)
	return !end.IsZero() && !now.Before(end)
}
