// Copyright (c) 2026 Arie Radle. MIT License.

package controller

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/meta"

	shekelv1alpha1 "github.com/arieradle/shekel-controller/api/v1alpha1"
)

// setCondition upserts a metav1.Condition on the ShekelBudget status.
func setCondition(budget *shekelv1alpha1.ShekelBudget, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&budget.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.NewTime(time.Now()),
		ObservedGeneration: budget.Generation,
	})
}
