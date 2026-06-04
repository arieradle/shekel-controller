// Copyright (c) 2026 Arie Radle. MIT License.

package controller

import (
	"testing"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	shekelv1alpha1 "github.com/arieradle/shekel-controller/api/v1alpha1"
)

func spendReport(name, spentUSD, callCount, lastUpdated, group string) corev1.ConfigMap {
	labels := map[string]string{
		labelSpendReport: "true",
		labelBudget:      "test-budget",
	}
	if group != "" {
		labels[labelGroup] = group
	}
	return corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Data: map[string]string{
			"spent_usd":    spentUSD,
			"call_count":   callCount,
			"last_updated": lastUpdated,
		},
	}
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }
func old() string { return time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339) }

func TestAggregateSpend_Empty(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	result := aggregateSpend(nil, time.Now().Add(-60*time.Second))
	g.Expect(result.TotalSpent).To(BeZero())
	g.Expect(result.ParticipatingPods).To(BeZero())
	g.Expect(result.CallCount).To(BeZero())
}

func TestAggregateSpend_Sum(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	reports := []corev1.ConfigMap{
		spendReport("pod-1", "10.5", "5", now(), ""),
		spendReport("pod-2", "3.25", "2", now(), ""),
		spendReport("pod-3", "0.75", "1", now(), ""),
	}
	result := aggregateSpend(reports, time.Now().Add(-60*time.Second))
	g.Expect(result.TotalSpent).To(BeNumerically("~", 14.5, 0.001))
	g.Expect(result.CallCount).To(Equal(8))
	g.Expect(result.ParticipatingPods).To(Equal(3))
}

func TestAggregateSpend_StaleExcluded(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	reports := []corev1.ConfigMap{
		spendReport("fresh", "10.0", "3", now(), ""),
		spendReport("stale", "999.0", "100", old(), ""),
	}
	result := aggregateSpend(reports, time.Now().Add(-60*time.Second))
	g.Expect(result.TotalSpent).To(BeNumerically("~", 10.0, 0.001))
	g.Expect(result.ParticipatingPods).To(Equal(1))
}

func TestAggregateSpend_Groups(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	reports := []corev1.ConfigMap{
		spendReport("pod-a1", "5.0", "1", now(), "groupA"),
		spendReport("pod-a2", "3.0", "1", now(), "groupA"),
		spendReport("pod-b1", "7.0", "1", now(), "groupB"),
		spendReport("pod-none", "1.0", "1", now(), ""),
	}
	result := aggregateSpend(reports, time.Now().Add(-60*time.Second))
	g.Expect(result.Groups["groupA"]).To(BeNumerically("~", 8.0, 0.001))
	g.Expect(result.Groups["groupB"]).To(BeNumerically("~", 7.0, 0.001))
	g.Expect(result.Groups).NotTo(HaveKey(""))
}

func TestAggregateSpend_LastFlush(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	recent := time.Now().Add(-5 * time.Second).UTC().Format(time.RFC3339)
	older := time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339)
	reports := []corev1.ConfigMap{
		spendReport("pod-1", "1.0", "1", older, ""),
		spendReport("pod-2", "2.0", "1", recent, ""),
	}
	result := aggregateSpend(reports, time.Now().Add(-60*time.Second))
	g.Expect(result.LastFlush.IsZero()).To(BeFalse())
	// LastFlush should be the most recent timestamp
	g.Expect(result.LastFlush.After(parseTime(older))).To(BeTrue())
}

func TestStaleCutoff_Default(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	spec := shekelv1alpha1.ShekelBudgetSpec{}
	cutoff := staleCutoff(spec)
	expected := time.Now().Add(-defaultFlushSeconds * time.Second)
	g.Expect(cutoff).To(BeTemporally("~", expected, 2*time.Second))
}

func TestStaleCutoff_Configured(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	secs := int32(15)
	spec := shekelv1alpha1.ShekelBudgetSpec{
		Enforcement: shekelv1alpha1.EnforcementSpec{FlushEverySeconds: &secs},
	}
	cutoff := staleCutoff(spec)
	expected := time.Now().Add(-30 * time.Second) // 2 × 15
	g.Expect(cutoff).To(BeTemporally("~", expected, 2*time.Second))
}

func TestWarnAtOrDefault_Nil(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	spec := shekelv1alpha1.ShekelBudgetSpec{}
	g.Expect(warnAtOrDefault(spec)).To(Equal(0.8))
}

func TestWarnAtOrDefault_Set(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	v := 0.75
	spec := shekelv1alpha1.ShekelBudgetSpec{WarnAt: &v}
	g.Expect(warnAtOrDefault(spec)).To(Equal(0.75))
}
