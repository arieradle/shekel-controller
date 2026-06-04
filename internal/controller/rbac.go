// Copyright (c) 2026 Arie Radle. MIT License.

package controller

import (
	"context"
	"fmt"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	shekelv1alpha1 "github.com/arieradle/shekel-controller/api/v1alpha1"
)

func podRoleName(budgetName string) string {
	return fmt.Sprintf("shekel-pod-reporter-%s", budgetName)
}

// buildPodRole returns the Role that grants pods permission to read the Budget
// ConfigMap and create/update Spend Report ConfigMaps in the budget's namespace.
func buildPodRole(budget *shekelv1alpha1.ShekelBudget) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podRoleName(budget.Name),
			Namespace: budget.Namespace,
			Labels: map[string]string{
				"shekel.dev/managed-by": "shekel-controller",
				"shekel.dev/budget":     budget.Name,
			},
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"configmaps"},
				Verbs:     []string{"get", "create", "patch", "update"},
			},
		},
	}
}

// buildPodRoleBinding binds the pod reporter Role to the budget's configured
// ServiceAccount (spec.podServiceAccount, defaults to "default").
func buildPodRoleBinding(budget *shekelv1alpha1.ShekelBudget) *rbacv1.RoleBinding {
	sa := budget.Spec.PodServiceAccount
	if sa == "" {
		sa = "default"
	}
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podRoleName(budget.Name),
			Namespace: budget.Namespace,
			Labels: map[string]string{
				"shekel.dev/managed-by": "shekel-controller",
				"shekel.dev/budget":     budget.Name,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     podRoleName(budget.Name),
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      sa,
				Namespace: budget.Namespace,
			},
		},
	}
}

// ensurePodRBAC creates or updates the Role and RoleBinding for pod reporters.
// Both objects are owner-referenced to the ShekelBudget and garbage-collected
// when the budget is deleted.
func ensurePodRBAC(ctx context.Context, c client.Client, budget *shekelv1alpha1.ShekelBudget, scheme *runtime.Scheme) error {
	if err := ensureRole(ctx, c, budget, scheme); err != nil {
		return err
	}
	return ensureRoleBinding(ctx, c, budget, scheme)
}

func ensureRole(ctx context.Context, c client.Client, budget *shekelv1alpha1.ShekelBudget, scheme *runtime.Scheme) error {
	desired := buildPodRole(budget)
	if err := ctrl.SetControllerReference(budget, desired, scheme); err != nil {
		return err
	}

	existing := &rbacv1.Role{}
	err := c.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return c.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	existing.Rules = desired.Rules
	return c.Update(ctx, existing)
}

func ensureRoleBinding(ctx context.Context, c client.Client, budget *shekelv1alpha1.ShekelBudget, scheme *runtime.Scheme) error {
	desired := buildPodRoleBinding(budget)
	if err := ctrl.SetControllerReference(budget, desired, scheme); err != nil {
		return err
	}

	existing := &rbacv1.RoleBinding{}
	err := c.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return c.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	existing.Subjects = desired.Subjects
	existing.RoleRef = desired.RoleRef
	return c.Update(ctx, existing)
}
