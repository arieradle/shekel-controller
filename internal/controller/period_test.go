// Copyright (c) 2026 Arie Radle. MIT License.

package controller

import (
	"testing"
	"time"

	. "github.com/onsi/gomega"

	shekelv1alpha1 "github.com/arieradle/shekel-controller/api/v1alpha1"
)

func TestComputePeriodEnd_Daily(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	// Start at 2026-06-04 14:30:00 UTC
	start := time.Date(2026, 6, 4, 14, 30, 0, 0, time.UTC)
	end := computePeriodEnd(shekelv1alpha1.PeriodDaily, start)
	// Should be 2026-06-05 00:00:00 UTC
	g.Expect(end).To(Equal(time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)))
}

func TestComputePeriodEnd_Daily_AlreadyMidnight(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	start := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	end := computePeriodEnd(shekelv1alpha1.PeriodDaily, start)
	g.Expect(end).To(Equal(time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)))
}

func TestComputePeriodEnd_Monthly(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	start := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	end := computePeriodEnd(shekelv1alpha1.PeriodMonthly, start)
	g.Expect(end).To(Equal(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)))
}

func TestComputePeriodEnd_Monthly_December(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	start := time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC)
	end := computePeriodEnd(shekelv1alpha1.PeriodMonthly, start)
	g.Expect(end).To(Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)))
}

func TestComputePeriodEnd_Rolling24h(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	start := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	end := computePeriodEnd(shekelv1alpha1.PeriodRolling24h, start)
	g.Expect(end).To(Equal(time.Date(2026, 6, 5, 10, 0, 0, 0, time.UTC)))
}

func TestComputePeriodEnd_None(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	start := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	end := computePeriodEnd(shekelv1alpha1.PeriodNone, start)
	g.Expect(end.IsZero()).To(BeTrue())
}

func TestPeriodResetDue(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)

	start := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)

	// none: never due
	g.Expect(periodResetDue(shekelv1alpha1.PeriodNone, start, start.Add(48*time.Hour))).To(BeFalse())

	// daily: before midnight → not due
	before := time.Date(2026, 6, 4, 23, 59, 0, 0, time.UTC)
	g.Expect(periodResetDue(shekelv1alpha1.PeriodDaily, start, before)).To(BeFalse())

	// daily: after midnight → due
	after := time.Date(2026, 6, 5, 0, 1, 0, 0, time.UTC)
	g.Expect(periodResetDue(shekelv1alpha1.PeriodDaily, start, after)).To(BeTrue())

	// monthly: mid-month → not due
	midMonth := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	g.Expect(periodResetDue(shekelv1alpha1.PeriodMonthly, start, midMonth)).To(BeFalse())

	// monthly: next month → due
	nextMonth := time.Date(2026, 7, 1, 0, 1, 0, 0, time.UTC)
	g.Expect(periodResetDue(shekelv1alpha1.PeriodMonthly, start, nextMonth)).To(BeTrue())

	// rolling-24h: 23h later → not due
	h23 := start.Add(23 * time.Hour)
	g.Expect(periodResetDue(shekelv1alpha1.PeriodRolling24h, start, h23)).To(BeFalse())

	// rolling-24h: 25h later → due
	h25 := start.Add(25 * time.Hour)
	g.Expect(periodResetDue(shekelv1alpha1.PeriodRolling24h, start, h25)).To(BeTrue())
}
