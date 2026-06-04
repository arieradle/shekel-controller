// Copyright (c) 2026 Arie Radle. MIT License.

package controller

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	shekelv1alpha1 "github.com/arieradle/shekel-controller/api/v1alpha1"
)

const configMapNamePrefix = "shekel-budget-"

func configMapName(budgetName string) string {
	return configMapNamePrefix + budgetName
}

// buildConfigMap constructs the desired Budget ConfigMap from a ShekelBudget spec.
// All values are strings. Optional fields are omitted when nil or empty.
// The caller is responsible for setting the owner reference.
func buildConfigMap(budget *shekelv1alpha1.ShekelBudget) *corev1.ConfigMap {
	spec := budget.Spec
	data := map[string]string{
		"budget_name":      budget.Name,
		"budget_namespace": budget.Namespace,
		"max_usd":          fmt.Sprintf("%.4f", spec.MaxUsd),
		"period":           string(spec.Period),
		"scope_mode":       string(spec.Scope.Mode),
		"backend":          string(spec.Enforcement.Backend),
		"paused":           "false",
	}

	if spec.WarnAt != nil {
		data["warn_at"] = fmt.Sprintf("%.4f", *spec.WarnAt)
	}
	if spec.Fallback != nil {
		data["fallback_model"] = spec.Fallback.Model
		data["fallback_at_pct"] = fmt.Sprintf("%.4f", spec.Fallback.AtPct)
	}
	if spec.Scope.GroupBy != "" {
		data["scope_group_by"] = spec.Scope.GroupBy
	}
	if spec.Scope.PerPodCap != nil {
		data["per_pod_cap"] = fmt.Sprintf("%.4f", *spec.Scope.PerPodCap)
	}
	if spec.Enforcement.FlushEveryUsd != nil {
		data["flush_every_usd"] = fmt.Sprintf("%.4f", *spec.Enforcement.FlushEveryUsd)
	}
	if spec.Enforcement.FlushEverySeconds != nil {
		data["flush_every_seconds"] = fmt.Sprintf("%d", *spec.Enforcement.FlushEverySeconds)
	}
	if spec.MaxLLMCalls != nil {
		data["max_llm_calls"] = fmt.Sprintf("%d", *spec.MaxLLMCalls)
	}
	if spec.Enforcement.Backend == shekelv1alpha1.BackendRedis {
		data["redis_key"] = fmt.Sprintf("shekel:%s:%s", budget.Namespace, budget.Name)
		data["redis_key_group_tpl"] = fmt.Sprintf("shekel:%s:%s:{group}", budget.Namespace, budget.Name)
	}

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName(budget.Name),
			Namespace: budget.Namespace,
			Labels: map[string]string{
				"shekel.dev/managed-by": "shekel-controller",
				"shekel.dev/budget":     budget.Name,
			},
		},
		Data: data,
	}
}

// mapsEqual returns true if both string maps have identical keys and values.
func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
