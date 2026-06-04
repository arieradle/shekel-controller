// Copyright (c) 2026 Arie Radle. MIT License.

package controller

import (
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"

	shekelv1alpha1 "github.com/arieradle/shekel-controller/api/v1alpha1"
)

const (
	labelSpendReport = "shekel.dev/spend-report"
	labelBudget      = "shekel.dev/budget"
	labelGroup       = "shekel.dev/group"
	labelCMType      = "shekel.dev/cm-type"
	cmTypeBudgetGroup = "budget-group"

	defaultFlushSeconds = 60
)

// AggregationResult holds totals across all live Spend Report ConfigMaps.
type AggregationResult struct {
	TotalSpent        float64
	CallCount         int
	ParticipatingPods int
	LastFlush         time.Time
	// Groups maps group label value → aggregated USD spent for that group.
	Groups map[string]float64
}

// aggregateSpend sums spend across all non-stale Spend Report ConfigMaps.
// Reports whose last_updated timestamp predates cutoff are silently excluded.
func aggregateSpend(reports []corev1.ConfigMap, cutoff time.Time) AggregationResult {
	result := AggregationResult{Groups: make(map[string]float64)}

	for _, cm := range reports {
		lastUpdated := parseTime(cm.Data["last_updated"])
		if !lastUpdated.IsZero() && lastUpdated.Before(cutoff) {
			continue // stale
		}

		spent := parseFloat(cm.Data["spent_usd"])
		calls := parseInt(cm.Data["call_count"])
		result.TotalSpent += spent
		result.CallCount += calls
		result.ParticipatingPods++

		if !lastUpdated.IsZero() && lastUpdated.After(result.LastFlush) {
			result.LastFlush = lastUpdated
		}

		if group := groupByLabel(cm); group != "" {
			result.Groups[group] += spent
		}
	}

	return result
}

// staleCutoff returns the earliest last_updated time a report may have and
// still be considered live. Defaults to now-60s; uses 2×flushEverySeconds
// when configured.
func staleCutoff(spec shekelv1alpha1.ShekelBudgetSpec) time.Time {
	seconds := defaultFlushSeconds
	if spec.Enforcement.FlushEverySeconds != nil && *spec.Enforcement.FlushEverySeconds > 0 {
		seconds = int(*spec.Enforcement.FlushEverySeconds) * 2
	}
	return time.Now().Add(-time.Duration(seconds) * time.Second)
}

// groupByLabel returns the value of the shekel.dev/group label on a ConfigMap,
// or "" if the label is absent.
func groupByLabel(cm corev1.ConfigMap) string {
	return cm.Labels[labelGroup]
}

// warnAtOrDefault returns the WarnAt fraction from the spec, or 0.8 if unset.
func warnAtOrDefault(spec shekelv1alpha1.ShekelBudgetSpec) float64 {
	if spec.WarnAt != nil {
		return *spec.WarnAt
	}
	return 0.8
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func parseInt(s string) int {
	v, _ := strconv.Atoi(s)
	return v
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
