// Copyright (c) 2026 Arie Radle. MIT License.

package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

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
		return ctrl.Result{}, r.setReconciled(ctx, budget, "ConfigMapCreated", "")
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	if mapsEqual(desired.Data, existing.Data) {
		return ctrl.Result{}, nil
	}

	logger.Info("updating ConfigMap", "configmap", existing.Name)
	existing.Data = desired.Data
	if err := r.Update(ctx, existing); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.setReconciled(ctx, budget, "ConfigMapUpdated", "")
}

func (r *ShekelBudgetReconciler) setReconciled(ctx context.Context, budget *shekelv1alpha1.ShekelBudget, reason, message string) error {
	patch := client.MergeFrom(budget.DeepCopy())
	setCondition(budget, "Reconciled", metav1.ConditionTrue, reason, message)
	return r.Status().Patch(ctx, budget, patch)
}

func (r *ShekelBudgetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&shekelv1alpha1.ShekelBudget{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}
