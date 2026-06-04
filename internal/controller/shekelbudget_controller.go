// Copyright (c) 2026 Arie Radle. MIT License.

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	shekelv1alpha1 "github.com/arieradle/shekel-controller/api/v1alpha1"
)

// ShekelBudgetReconciler reconciles ShekelBudget objects.
//
// +kubebuilder:rbac:groups=shekel.dev,resources=shekelbudgets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=shekel.dev,resources=shekelbudgets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=shekel.dev,resources=shekelbudgets/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete
type ShekelBudgetReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *ShekelBudgetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("budget", req.NamespacedName)

	budget := &shekelv1alpha1.ShekelBudget{}
	if err := r.Get(ctx, req.NamespacedName, budget); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// --- Step 1: Ensure Budget ConfigMap ---
	desired := buildConfigMap(budget)
	if err := ctrl.SetControllerReference(budget, desired, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	existing := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)

	if apierrors.IsNotFound(err) {
		logger.Info("creating ConfigMap", "configmap", desired.Name)
		if err := r.Create(ctx, desired); err != nil {
			return ctrl.Result{}, err
		}
		existing = desired
		if err := r.setReconciled(ctx, budget, "ConfigMapCreated", ""); err != nil {
			return ctrl.Result{}, err
		}
	} else if err != nil {
		return ctrl.Result{}, err
	} else if !mapsEqual(desired.Data, existing.Data) {
		logger.Info("updating ConfigMap", "configmap", existing.Name)
		existing.Data = desired.Data
		if err := r.Update(ctx, existing); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.setReconciled(ctx, budget, "ConfigMapUpdated", ""); err != nil {
			return ctrl.Result{}, err
		}
	}

	// --- Step 2: Ensure pod RBAC ---
	if err := ensurePodRBAC(ctx, r.Client, budget, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	// --- Step 3: Initialise periodStart on first reconcile ---
	if budget.Status.PeriodStart == nil {
		now := metav1.NewTime(time.Now())
		patch := client.MergeFrom(budget.DeepCopy())
		budget.Status.PeriodStart = &now
		if err := r.Status().Patch(ctx, budget, patch); err != nil {
			return ctrl.Result{}, err
		}
	}

	// --- Step 4: Period reset ---
	if periodResetDue(budget.Spec.Period, budget.Status.PeriodStart.Time, time.Now()) {
		logger.Info("period reset", "period", budget.Spec.Period)
		if err := r.resetPeriod(ctx, budget); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// --- Step 5: Aggregate spend ---
	reportList := &corev1.ConfigMapList{}
	if err := r.List(ctx, reportList, client.InNamespace(budget.Namespace),
		client.MatchingLabels{
			labelBudget:      budget.Name,
			labelSpendReport: "true",
		}); err != nil {
		return ctrl.Result{}, err
	}

	result := aggregateSpend(reportList.Items, staleCutoff(budget.Spec))

	// --- Step 6: Determine state ---
	warnThreshold := budget.Spec.MaxUsd * warnAtOrDefault(budget.Spec)
	exceeded := result.TotalSpent >= budget.Spec.MaxUsd
	warned := !exceeded && result.TotalSpent >= warnThreshold

	// --- Step 7: Update Budget ConfigMap paused key ---
	currentPaused := existing.Data["paused"]
	if exceeded && currentPaused != "true" {
		logger.Info("activating kill-switch", "spent", result.TotalSpent, "limit", budget.Spec.MaxUsd)
		existing.Data["paused"] = "true"
		if err := r.Update(ctx, existing); err != nil {
			return ctrl.Result{}, err
		}
	} else if !exceeded && currentPaused == "true" {
		logger.Info("clearing kill-switch", "spent", result.TotalSpent)
		existing.Data["paused"] = "false"
		if err := r.Update(ctx, existing); err != nil {
			return ctrl.Result{}, err
		}
	}

	// --- Step 8: Update status conditions ---
	budgetPatch := client.MergeFrom(budget.DeepCopy())

	warnStatus := metav1.ConditionFalse
	if warned || exceeded {
		warnStatus = metav1.ConditionTrue
	}
	setCondition(budget, "BudgetWarning", warnStatus, conditionReason("BudgetWarning", warned || exceeded), "")

	pausedStatus := metav1.ConditionFalse
	if exceeded {
		pausedStatus = metav1.ConditionTrue
	}
	setCondition(budget, "BudgetPaused", pausedStatus, conditionReason("BudgetPaused", exceeded), "")

	// --- Step 9: Update status fields ---
	budget.Status.TotalSpent = &result.TotalSpent
	budget.Status.ParticipatingPods = int32(result.ParticipatingPods)
	if !result.LastFlush.IsZero() {
		t := metav1.NewTime(result.LastFlush)
		budget.Status.LastFlush = &t
	}

	// --- Step 10: Per-group ConfigMaps and status.groups ---
	if err := r.reconcileGroups(ctx, budget, result.Groups); err != nil {
		return ctrl.Result{}, err
	}

	// Build status.groups slice
	budget.Status.Groups = make([]shekelv1alpha1.GroupStatus, 0, len(result.Groups))
	for k, v := range result.Groups {
		budget.Status.Groups = append(budget.Status.Groups, shekelv1alpha1.GroupStatus{Key: k, Spent: v})
	}

	if err := r.Status().Patch(ctx, budget, budgetPatch); err != nil {
		return ctrl.Result{}, err
	}

	// --- Step 11: Requeue at period end ---
	if budget.Spec.Period != shekelv1alpha1.PeriodNone && budget.Status.PeriodStart != nil {
		periodEnd := computePeriodEnd(budget.Spec.Period, budget.Status.PeriodStart.Time)
		if !periodEnd.IsZero() {
			return ctrl.Result{RequeueAfter: time.Until(periodEnd)}, nil
		}
	}

	return ctrl.Result{}, nil
}

// reconcileGroups creates or updates per-group Budget ConfigMaps and applies
// the kill-switch independently per group.
func (r *ShekelBudgetReconciler) reconcileGroups(ctx context.Context, budget *shekelv1alpha1.ShekelBudget, groups map[string]float64) error {
	for groupKey, groupSpent := range groups {
		cmName := fmt.Sprintf("%s%s-%s", configMapNamePrefix, budget.Name, groupKey)
		cm := &corev1.ConfigMap{}
		err := r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: budget.Namespace}, cm)
		if apierrors.IsNotFound(err) {
			cm = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cmName,
					Namespace: budget.Namespace,
					Labels: map[string]string{
						"shekel.dev/managed-by": "shekel-controller",
						"shekel.dev/budget":     budget.Name,
						labelGroup:              groupKey,
						labelCMType:             cmTypeBudgetGroup,
					},
				},
				Data: map[string]string{
					"budget_name": budget.Name,
					"group":       groupKey,
					"paused":      "false",
				},
			}
			if err := ctrl.SetControllerReference(budget, cm, r.Scheme); err != nil {
				return err
			}
			if err := r.Create(ctx, cm); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}

		desiredPaused := "false"
		if groupSpent >= budget.Spec.MaxUsd {
			desiredPaused = "true"
		}
		if cm.Data["paused"] != desiredPaused {
			if cm.Data == nil {
				cm.Data = map[string]string{}
			}
			cm.Data["paused"] = desiredPaused
			if err := r.Update(ctx, cm); err != nil {
				return err
			}
		}
	}
	return nil
}

// resetPeriod zeroes all spend tracking and restores the kill-switch.
func (r *ShekelBudgetReconciler) resetPeriod(ctx context.Context, budget *shekelv1alpha1.ShekelBudget) error {
	// Delete all Spend Report ConfigMaps for this budget.
	reportList := &corev1.ConfigMapList{}
	if err := r.List(ctx, reportList, client.InNamespace(budget.Namespace),
		client.MatchingLabels{labelBudget: budget.Name, labelSpendReport: "true"}); err != nil {
		return err
	}
	for i := range reportList.Items {
		if err := r.Delete(ctx, &reportList.Items[i]); client.IgnoreNotFound(err) != nil {
			return err
		}
	}

	// Clear group Budget CMs (set paused=false).
	groupCMList := &corev1.ConfigMapList{}
	if err := r.List(ctx, groupCMList, client.InNamespace(budget.Namespace),
		client.MatchingLabels{labelBudget: budget.Name, labelCMType: cmTypeBudgetGroup}); err != nil {
		return err
	}
	for i := range groupCMList.Items {
		cm := &groupCMList.Items[i]
		if cm.Data["paused"] != "false" {
			if cm.Data == nil {
				cm.Data = map[string]string{}
			}
			cm.Data["paused"] = "false"
			if err := r.Update(ctx, cm); err != nil {
				return err
			}
		}
	}

	// Reset status.
	patch := client.MergeFrom(budget.DeepCopy())
	zero := float64(0)
	now := metav1.NewTime(time.Now())
	budget.Status.TotalSpent = &zero
	budget.Status.ParticipatingPods = 0
	budget.Status.PeriodStart = &now
	budget.Status.Groups = nil
	setCondition(budget, "BudgetWarning", metav1.ConditionFalse, "PeriodReset", "")
	setCondition(budget, "BudgetPaused", metav1.ConditionFalse, "PeriodReset", "")
	return r.Status().Patch(ctx, budget, patch)
}

func (r *ShekelBudgetReconciler) setReconciled(ctx context.Context, budget *shekelv1alpha1.ShekelBudget, reason, message string) error {
	patch := client.MergeFrom(budget.DeepCopy())
	setCondition(budget, "Reconciled", metav1.ConditionTrue, reason, message)
	return r.Status().Patch(ctx, budget, patch)
}

// spendReportToShekelBudget maps a Spend Report ConfigMap event to the
// owning ShekelBudget reconcile request.
func (r *ShekelBudgetReconciler) spendReportToShekelBudget(_ context.Context, obj client.Object) []reconcile.Request {
	budgetName, ok := obj.GetLabels()[labelBudget]
	if !ok || budgetName == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Name:      budgetName,
			Namespace: obj.GetNamespace(),
		},
	}}
}

// isSpendReport returns true for ConfigMaps carrying the spend-report label.
func isSpendReport(obj client.Object) bool {
	return obj.GetLabels()[labelSpendReport] == "true"
}

func conditionReason(condType string, active bool) string {
	if active {
		return condType + "Active"
	}
	return condType + "Inactive"
}

func (r *ShekelBudgetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&shekelv1alpha1.ShekelBudget{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Watches(
			&corev1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(r.spendReportToShekelBudget),
			builder.WithPredicates(predicate.NewPredicateFuncs(isSpendReport)),
		).
		Complete(r)
}
