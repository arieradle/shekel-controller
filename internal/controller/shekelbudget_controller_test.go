// Copyright (c) 2026 Arie Radle. MIT License.

package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	shekelv1alpha1 "github.com/arieradle/shekel-controller/api/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	g := NewWithT(t)
	g.Expect(clientgoscheme.AddToScheme(s)).To(Succeed())
	g.Expect(corev1.AddToScheme(s)).To(Succeed())
	g.Expect(rbacv1.AddToScheme(s)).To(Succeed())
	g.Expect(shekelv1alpha1.AddToScheme(s)).To(Succeed())
	return s
}

// makeSpendReport builds a Spend Report ConfigMap for use in fake client tests.
func makeSpendReport(podName, budgetName, ns, spentUSD string, age time.Duration, group string) *corev1.ConfigMap {
	labels := map[string]string{
		labelSpendReport: "true",
		labelBudget:      budgetName,
	}
	if group != "" {
		labels[labelGroup] = group
	}
	lastUpdated := time.Now().Add(-age).UTC().Format(time.RFC3339)
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shekel-spend-" + podName,
			Namespace: ns,
			Labels:    labels,
		},
		Data: map[string]string{
			"spent_usd":    spentUSD,
			"call_count":   "1",
			"last_updated": lastUpdated,
			"pod_name":     podName,
		},
	}
}

func minimalBudget(name, ns string) *shekelv1alpha1.ShekelBudget {
	return &shekelv1alpha1.ShekelBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			UID:       "test-uid-1234",
		},
		Spec: shekelv1alpha1.ShekelBudgetSpec{
			MaxUsd: 100.0,
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
		},
	}
}

func reconciler(scheme *runtime.Scheme, objs ...runtime.Object) *ShekelBudgetReconciler {
	clientObjs := make([]client.Object, len(objs))
	for i, o := range objs {
		clientObjs[i] = o.(client.Object)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(clientObjs...).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		Build()
	return &ShekelBudgetReconciler{Client: c, Scheme: scheme}
}

func req(name, ns string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
}

// ── Reconcile tests ───────────────────────────────────────────────────────────

func TestReconcile_NotFound(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	r := reconciler(s) // no objects

	result, err := r.Reconcile(context.Background(), req("missing", "default"))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(ctrl.Result{}))

	// No ConfigMap should have been created
	cm := &corev1.ConfigMap{}
	err = r.Get(context.Background(), types.NamespacedName{Name: "shekel-budget-missing", Namespace: "default"}, cm)
	g.Expect(err).To(HaveOccurred())
}

func TestReconcile_CreateConfigMap(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("test-budget", "default")
	r := reconciler(s, budget)

	_, err := r.Reconcile(context.Background(), req("test-budget", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	cm := &corev1.ConfigMap{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "shekel-budget-test-budget", Namespace: "default"}, cm)).To(Succeed())
	g.Expect(cm.Data["budget_name"]).To(Equal("test-budget"))
	g.Expect(cm.Data["budget_namespace"]).To(Equal("default"))
	g.Expect(cm.Data["max_usd"]).To(Equal("100.0000"))
	g.Expect(cm.Data["paused"]).To(Equal("false"))
}

func TestReconcile_AllOptionalFields(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)

	warnAt := 0.8
	perPodCap := 10.0
	flushUsd := 1.0
	flushSecs := int32(30)
	budget := minimalBudget("full-budget", "default")
	budget.Spec.Period = shekelv1alpha1.PeriodMonthly
	budget.Spec.WarnAt = &warnAt
	budget.Spec.Fallback = &shekelv1alpha1.FallbackSpec{Model: "gpt-4o-mini", AtPct: 0.9}
	budget.Spec.Scope = shekelv1alpha1.ScopeSpec{
		Mode:      shekelv1alpha1.ScopeModePerGroup,
		GroupBy:   "tenant-id",
		PerPodCap: &perPodCap,
	}
	budget.Spec.Enforcement = shekelv1alpha1.EnforcementSpec{
		Backend:           shekelv1alpha1.BackendRedis,
		FlushEveryUsd:     &flushUsd,
		FlushEverySeconds: &flushSecs,
	}

	r := reconciler(s, budget)
	_, err := r.Reconcile(context.Background(), req("full-budget", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	cm := &corev1.ConfigMap{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "shekel-budget-full-budget", Namespace: "default"}, cm)).To(Succeed())
	g.Expect(cm.Data["period"]).To(Equal("monthly"))
	g.Expect(cm.Data["warn_at"]).To(Equal("0.8000"))
	g.Expect(cm.Data["fallback_model"]).To(Equal("gpt-4o-mini"))
	g.Expect(cm.Data["fallback_at_pct"]).To(Equal("0.9000"))
	g.Expect(cm.Data["scope_mode"]).To(Equal("per-group"))
	g.Expect(cm.Data["scope_group_by"]).To(Equal("tenant-id"))
	g.Expect(cm.Data["per_pod_cap"]).To(Equal("10.0000"))
	g.Expect(cm.Data["backend"]).To(Equal("redis"))
	g.Expect(cm.Data["flush_every_usd"]).To(Equal("1.0000"))
	g.Expect(cm.Data["flush_every_seconds"]).To(Equal("30"))
}

func TestReconcile_OmitsNilFields(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("minimal", "default") // no optional fields
	r := reconciler(s, budget)

	_, err := r.Reconcile(context.Background(), req("minimal", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	cm := &corev1.ConfigMap{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "shekel-budget-minimal", Namespace: "default"}, cm)).To(Succeed())
	g.Expect(cm.Data).NotTo(HaveKey("warn_at"))
	g.Expect(cm.Data).NotTo(HaveKey("fallback_model"))
	g.Expect(cm.Data).NotTo(HaveKey("fallback_at_pct"))
	g.Expect(cm.Data).NotTo(HaveKey("scope_group_by"))
	g.Expect(cm.Data).NotTo(HaveKey("per_pod_cap"))
	g.Expect(cm.Data).NotTo(HaveKey("flush_every_usd"))
	g.Expect(cm.Data).NotTo(HaveKey("flush_every_seconds"))
}

func TestReconcile_OwnerReference(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("owned", "default")
	r := reconciler(s, budget)

	_, err := r.Reconcile(context.Background(), req("owned", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	cm := &corev1.ConfigMap{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "shekel-budget-owned", Namespace: "default"}, cm)).To(Succeed())
	g.Expect(cm.OwnerReferences).To(HaveLen(1))
	g.Expect(cm.OwnerReferences[0].Name).To(Equal("owned"))
	g.Expect(cm.OwnerReferences[0].UID).To(Equal(budget.UID))
	g.Expect(*cm.OwnerReferences[0].Controller).To(BeTrue())
}

func TestReconcile_Idempotent(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("idem", "default")
	r := reconciler(s, budget)

	// First reconcile creates the ConfigMap.
	_, err := r.Reconcile(context.Background(), req("idem", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	cm := &corev1.ConfigMap{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "shekel-budget-idem", Namespace: "default"}, cm)).To(Succeed())
	rvAfterCreate := cm.ResourceVersion

	// Second reconcile on unchanged budget must not call Update.
	_, err = r.Reconcile(context.Background(), req("idem", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "shekel-budget-idem", Namespace: "default"}, cm)).To(Succeed())
	g.Expect(cm.ResourceVersion).To(Equal(rvAfterCreate))
}

func TestReconcile_UpdateOnChange(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("changing", "default")
	r := reconciler(s, budget)

	_, err := r.Reconcile(context.Background(), req("changing", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	// Simulate a spec change by mutating budget in the fake store.
	fetched := &shekelv1alpha1.ShekelBudget{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "changing", Namespace: "default"}, fetched)).To(Succeed())
	fetched.Spec.MaxUsd = 250.0
	g.Expect(r.Update(context.Background(), fetched)).To(Succeed())

	_, err = r.Reconcile(context.Background(), req("changing", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	cm := &corev1.ConfigMap{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "shekel-budget-changing", Namespace: "default"}, cm)).To(Succeed())
	g.Expect(cm.Data["max_usd"]).To(Equal("250.0000"))
}

func TestReconcile_StatusConditionSet(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("conditioned", "default")
	r := reconciler(s, budget)

	_, err := r.Reconcile(context.Background(), req("conditioned", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	updated := &shekelv1alpha1.ShekelBudget{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "conditioned", Namespace: "default"}, updated)).To(Succeed())

	found := false
	for _, c := range updated.Status.Conditions {
		if c.Type == "Reconciled" && c.Status == metav1.ConditionTrue {
			found = true
			break
		}
	}
	g.Expect(found).To(BeTrue(), "expected Reconciled=True condition on budget status")
}

// ── buildConfigMap unit tests ─────────────────────────────────────────────────

func TestBuildConfigMap_Minimal(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	budget := minimalBudget("min", "ns1")
	cm := buildConfigMap(budget)

	g.Expect(cm.Name).To(Equal("shekel-budget-min"))
	g.Expect(cm.Namespace).To(Equal("ns1"))
	g.Expect(cm.Data["budget_name"]).To(Equal("min"))
	g.Expect(cm.Data["budget_namespace"]).To(Equal("ns1"))
	g.Expect(cm.Data["max_usd"]).To(Equal("100.0000"))
	g.Expect(cm.Data["paused"]).To(Equal("false"))
	g.Expect(cm.Labels["shekel.dev/managed-by"]).To(Equal("shekel-controller"))
	g.Expect(cm.Labels["shekel.dev/budget"]).To(Equal("min"))
}

func TestBuildConfigMap_Full(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	warnAt := 0.75
	perPodCap := 5.5
	flushUsd := 2.0
	flushSecs := int32(60)
	budget := minimalBudget("full", "ns2")
	budget.Spec.Period = shekelv1alpha1.PeriodDaily
	budget.Spec.WarnAt = &warnAt
	budget.Spec.Fallback = &shekelv1alpha1.FallbackSpec{Model: "gpt-3.5-turbo", AtPct: 0.85}
	budget.Spec.Scope = shekelv1alpha1.ScopeSpec{
		Mode:      shekelv1alpha1.ScopeModePerPod,
		GroupBy:   "app",
		PerPodCap: &perPodCap,
	}
	budget.Spec.Enforcement = shekelv1alpha1.EnforcementSpec{
		Backend:           shekelv1alpha1.BackendK8s,
		FlushEveryUsd:     &flushUsd,
		FlushEverySeconds: &flushSecs,
	}

	cm := buildConfigMap(budget)
	g.Expect(cm.Data["period"]).To(Equal("daily"))
	g.Expect(cm.Data["warn_at"]).To(Equal("0.7500"))
	g.Expect(cm.Data["fallback_model"]).To(Equal("gpt-3.5-turbo"))
	g.Expect(cm.Data["fallback_at_pct"]).To(Equal("0.8500"))
	g.Expect(cm.Data["scope_mode"]).To(Equal("per-pod"))
	g.Expect(cm.Data["scope_group_by"]).To(Equal("app"))
	g.Expect(cm.Data["per_pod_cap"]).To(Equal("5.5000"))
	g.Expect(cm.Data["backend"]).To(Equal("k8s"))
	g.Expect(cm.Data["flush_every_usd"]).To(Equal("2.0000"))
	g.Expect(cm.Data["flush_every_seconds"]).To(Equal("60"))
}

// ── Error path tests ──────────────────────────────────────────────────────────

func TestReconcile_GetBudgetError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("err-budget", "default")

	calls := 0
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(budget).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*shekelv1alpha1.ShekelBudget); ok {
					calls++
					if calls == 1 {
						return fmt.Errorf("transient API error")
					}
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), req("err-budget", "default"))
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("transient API error"))
}

func TestReconcile_GetConfigMapError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("cm-get-err", "default")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(budget).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.ConfigMap); ok {
					return fmt.Errorf("configmap store unavailable")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), req("cm-get-err", "default"))
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("configmap store unavailable"))
}

func TestReconcile_CreateError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("create-err", "default")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(budget).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*corev1.ConfigMap); ok {
					return fmt.Errorf("create failed")
				}
				return cl.Create(ctx, obj, opts...)
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), req("create-err", "default"))
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("create failed"))
}

func TestReconcile_UpdateError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("update-err", "default")

	// Pre-create the ConfigMap with different data so the reconciler tries to update it.
	existingCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "shekel-budget-update-err", Namespace: "default"},
		Data:       map[string]string{"max_usd": "0.0000", "stale": "true"},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(budget, existingCM).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, ok := obj.(*corev1.ConfigMap); ok {
					return fmt.Errorf("update failed")
				}
				return cl.Update(ctx, obj, opts...)
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), req("update-err", "default"))
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("update failed"))
}

func TestBuildConfigMap_MaxLLMCalls(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	calls := int32(1000)
	budget := minimalBudget("calls", "ns")
	budget.Spec.MaxLLMCalls = &calls
	cm := buildConfigMap(budget)
	g.Expect(cm.Data["max_llm_calls"]).To(Equal("1000"))
}

func TestBuildConfigMap_RedisKey(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	budget := minimalBudget("mybudget", "mynamespace")
	budget.Spec.Enforcement = shekelv1alpha1.EnforcementSpec{Backend: shekelv1alpha1.BackendRedis}
	cm := buildConfigMap(budget)
	g.Expect(cm.Data["redis_key"]).To(Equal("shekel:mynamespace:mybudget"))
	g.Expect(cm.Data["redis_key_group_tpl"]).To(Equal("shekel:mynamespace:mybudget:{group}"))
}

func TestBuildConfigMap_NoRedisKey(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	budget := minimalBudget("mybudget", "mynamespace")
	budget.Spec.Enforcement = shekelv1alpha1.EnforcementSpec{Backend: shekelv1alpha1.BackendK8s}
	cm := buildConfigMap(budget)
	g.Expect(cm.Data).NotTo(HaveKey("redis_key"))
	g.Expect(cm.Data).NotTo(HaveKey("redis_key_group_tpl"))
}

// ── SHEK-15: Spend aggregation, kill-switch, period reset, RBAC ───────────────

func TestReconcile_PeriodStartInitialized(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("period-init", "default")
	r := reconciler(s, budget)

	_, err := r.Reconcile(context.Background(), req("period-init", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	updated := &shekelv1alpha1.ShekelBudget{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "period-init", Namespace: "default"}, updated)).To(Succeed())
	g.Expect(updated.Status.PeriodStart).NotTo(BeNil())
}

func TestReconcile_KillSwitch_Triggered(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("ks-trigger", "default")  // maxUsd=100

	report := makeSpendReport("pod-a", "ks-trigger", "default", "100.01", 5*time.Second, "")
	r := reconciler(s, budget, report)

	_, err := r.Reconcile(context.Background(), req("ks-trigger", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	cm := &corev1.ConfigMap{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "shekel-budget-ks-trigger", Namespace: "default"}, cm)).To(Succeed())
	g.Expect(cm.Data["paused"]).To(Equal("true"))

	updated := &shekelv1alpha1.ShekelBudget{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "ks-trigger", Namespace: "default"}, updated)).To(Succeed())
	found := false
	for _, c := range updated.Status.Conditions {
		if c.Type == "BudgetPaused" && c.Status == metav1.ConditionTrue {
			found = true
		}
	}
	g.Expect(found).To(BeTrue(), "expected BudgetPaused=True")
}

func TestReconcile_KillSwitch_Warning(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("ks-warn", "default") // maxUsd=100, warnAt default=0.8 → warn at 80

	report := makeSpendReport("pod-a", "ks-warn", "default", "85.0", 5*time.Second, "")
	r := reconciler(s, budget, report)

	_, err := r.Reconcile(context.Background(), req("ks-warn", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	cm := &corev1.ConfigMap{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "shekel-budget-ks-warn", Namespace: "default"}, cm)).To(Succeed())
	g.Expect(cm.Data["paused"]).To(Equal("false"))

	updated := &shekelv1alpha1.ShekelBudget{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "ks-warn", Namespace: "default"}, updated)).To(Succeed())
	warnTrue, pausedTrue := false, false
	for _, c := range updated.Status.Conditions {
		if c.Type == "BudgetWarning" && c.Status == metav1.ConditionTrue {
			warnTrue = true
		}
		if c.Type == "BudgetPaused" && c.Status == metav1.ConditionTrue {
			pausedTrue = true
		}
	}
	g.Expect(warnTrue).To(BeTrue(), "expected BudgetWarning=True")
	g.Expect(pausedTrue).To(BeFalse(), "expected BudgetPaused=False")
}

func TestReconcile_KillSwitch_Restored(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	ctx := context.Background()

	budget := minimalBudget("ks-restore", "default")
	// Pre-create a Budget ConfigMap that is already paused.
	pausedCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "shekel-budget-ks-restore", Namespace: "default",
			Labels: map[string]string{"shekel.dev/managed-by": "shekel-controller", "shekel.dev/budget": "ks-restore"}},
		Data: map[string]string{"budget_name": "ks-restore", "max_usd": "100.0000", "paused": "true",
			"period": "none", "scope_mode": "shared", "backend": "k8s", "budget_namespace": "default"},
	}
	r := reconciler(s, budget, pausedCM)

	// No spend reports → spend=0 → kill-switch should be cleared.
	_, err := r.Reconcile(ctx, req("ks-restore", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	cm := &corev1.ConfigMap{}
	g.Expect(r.Get(ctx, types.NamespacedName{Name: "shekel-budget-ks-restore", Namespace: "default"}, cm)).To(Succeed())
	g.Expect(cm.Data["paused"]).To(Equal("false"))
}

func TestReconcile_StatusTotalSpent(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("status-spent", "default")
	r1 := makeSpendReport("pod-1", "status-spent", "default", "12.5", 5*time.Second, "")
	r2 := makeSpendReport("pod-2", "status-spent", "default", "7.5", 5*time.Second, "")
	r := reconciler(s, budget, r1, r2)

	_, err := r.Reconcile(context.Background(), req("status-spent", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	updated := &shekelv1alpha1.ShekelBudget{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "status-spent", Namespace: "default"}, updated)).To(Succeed())
	g.Expect(updated.Status.TotalSpent).NotTo(BeNil())
	g.Expect(*updated.Status.TotalSpent).To(BeNumerically("~", 20.0, 0.001))
	g.Expect(updated.Status.ParticipatingPods).To(Equal(int32(2)))
}

func TestReconcile_StaleReportsExcluded(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("stale-test", "default")
	fresh := makeSpendReport("pod-fresh", "stale-test", "default", "5.0", 10*time.Second, "")
	stale := makeSpendReport("pod-stale", "stale-test", "default", "999.0", 2*time.Hour, "")
	r := reconciler(s, budget, fresh, stale)

	_, err := r.Reconcile(context.Background(), req("stale-test", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	updated := &shekelv1alpha1.ShekelBudget{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "stale-test", Namespace: "default"}, updated)).To(Succeed())
	g.Expect(*updated.Status.TotalSpent).To(BeNumerically("~", 5.0, 0.001))
	g.Expect(updated.Status.ParticipatingPods).To(Equal(int32(1)))
}

func TestReconcile_PodRBACCreated(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("rbac-test", "default")
	r := reconciler(s, budget)

	_, err := r.Reconcile(context.Background(), req("rbac-test", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	role := &rbacv1.Role{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "shekel-pod-reporter-rbac-test", Namespace: "default"}, role)).To(Succeed())

	rb := &rbacv1.RoleBinding{}
	g.Expect(r.Get(context.Background(), types.NamespacedName{Name: "shekel-pod-reporter-rbac-test", Namespace: "default"}, rb)).To(Succeed())
	g.Expect(rb.RoleRef.Name).To(Equal("shekel-pod-reporter-rbac-test"))
}

func TestReconcile_PeriodReset(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	ctx := context.Background()

	budget := minimalBudget("reset-test", "default")
	budget.Spec.Period = shekelv1alpha1.PeriodDaily
	// Set periodStart to 25 hours ago so reset is due.
	past := metav1.NewTime(time.Now().Add(-25 * time.Hour))
	budget.Status.PeriodStart = &past

	spendReport := makeSpendReport("pod-a", "reset-test", "default", "50.0", 5*time.Second, "")
	r := reconciler(s, budget, spendReport)

	_, err := r.Reconcile(ctx, req("reset-test", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	// Spend Report ConfigMap should have been deleted.
	cmList := &corev1.ConfigMapList{}
	g.Expect(r.List(ctx, cmList, client.InNamespace("default"),
		client.MatchingLabels{labelBudget: "reset-test", labelSpendReport: "true"})).To(Succeed())
	g.Expect(cmList.Items).To(BeEmpty())

	// Status should be reset.
	updated := &shekelv1alpha1.ShekelBudget{}
	g.Expect(r.Get(ctx, types.NamespacedName{Name: "reset-test", Namespace: "default"}, updated)).To(Succeed())
	g.Expect(*updated.Status.TotalSpent).To(BeZero())
	g.Expect(updated.Status.PeriodStart.Time).To(BeTemporally(">", past.Time))
}

func TestReconcile_RequeueAtPeriodEnd(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("requeue-test", "default")
	budget.Spec.Period = shekelv1alpha1.PeriodDaily
	r := reconciler(s, budget)

	result, err := r.Reconcile(context.Background(), req("requeue-test", "default"))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.RequeueAfter).To(BeNumerically(">", 0))
	g.Expect(result.RequeueAfter).To(BeNumerically("<=", 25*time.Hour))
}

func TestReconcile_NoRequeueForNone(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("no-requeue", "default") // period defaults to "none"
	r := reconciler(s, budget)

	result, err := r.Reconcile(context.Background(), req("no-requeue", "default"))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result.RequeueAfter).To(Equal(time.Duration(0)))
}

func TestReconcile_PerGroup_IndependentPause(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	ctx := context.Background()

	budget := minimalBudget("group-test", "default") // maxUsd=100
	budget.Spec.Scope = shekelv1alpha1.ScopeSpec{Mode: shekelv1alpha1.ScopeModePerGroup, GroupBy: "team"}

	// Group A exceeded, Group B has not.
	rA1 := makeSpendReport("pod-a1", "group-test", "default", "60.0", 5*time.Second, "groupA")
	rA2 := makeSpendReport("pod-a2", "group-test", "default", "50.0", 5*time.Second, "groupA")
	rB := makeSpendReport("pod-b", "group-test", "default", "30.0", 5*time.Second, "groupB")
	r := reconciler(s, budget, rA1, rA2, rB)

	_, err := r.Reconcile(ctx, req("group-test", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	cmA := &corev1.ConfigMap{}
	g.Expect(r.Get(ctx, types.NamespacedName{Name: "shekel-budget-group-test-groupA", Namespace: "default"}, cmA)).To(Succeed())
	g.Expect(cmA.Data["paused"]).To(Equal("true"), "groupA should be paused")

	cmB := &corev1.ConfigMap{}
	g.Expect(r.Get(ctx, types.NamespacedName{Name: "shekel-budget-group-test-groupB", Namespace: "default"}, cmB)).To(Succeed())
	g.Expect(cmB.Data["paused"]).To(Equal("false"), "groupB should not be paused")
}

func TestReconcile_PodRBACIdempotent(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("rbac-idem", "default")
	r := reconciler(s, budget)
	ctx := context.Background()

	// First reconcile creates Role + RoleBinding.
	_, err := r.Reconcile(ctx, req("rbac-idem", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	role := &rbacv1.Role{}
	g.Expect(r.Get(ctx, types.NamespacedName{Name: "shekel-pod-reporter-rbac-idem", Namespace: "default"}, role)).To(Succeed())
	rv1 := role.ResourceVersion

	// Second reconcile updates (hits the "exists" branch).
	_, err = r.Reconcile(ctx, req("rbac-idem", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(r.Get(ctx, types.NamespacedName{Name: "shekel-pod-reporter-rbac-idem", Namespace: "default"}, role)).To(Succeed())
	// ResourceVersion should have changed (update was called)
	g.Expect(role.ResourceVersion).NotTo(Equal(rv1))
}

func TestReconcile_PeriodReset_ClearsGroupCMs(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	ctx := context.Background()

	budget := minimalBudget("grp-reset", "default")
	budget.Spec.Period = shekelv1alpha1.PeriodDaily
	past := metav1.NewTime(time.Now().Add(-25 * time.Hour))
	budget.Status.PeriodStart = &past

	// Pre-create a paused group Budget ConfigMap.
	groupCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shekel-budget-grp-reset-teamA",
			Namespace: "default",
			Labels: map[string]string{
				labelBudget: "grp-reset",
				labelCMType: cmTypeBudgetGroup,
				labelGroup:  "teamA",
			},
		},
		Data: map[string]string{"paused": "true"},
	}
	r := reconciler(s, budget, groupCM)

	_, err := r.Reconcile(ctx, req("grp-reset", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	// Group CM paused should be cleared to false.
	cm := &corev1.ConfigMap{}
	g.Expect(r.Get(ctx, types.NamespacedName{Name: "shekel-budget-grp-reset-teamA", Namespace: "default"}, cm)).To(Succeed())
	g.Expect(cm.Data["paused"]).To(Equal("false"))
}

func TestReconcile_GroupCMUpdated(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	ctx := context.Background()

	budget := minimalBudget("grp-update", "default")
	budget.Spec.Scope = shekelv1alpha1.ScopeSpec{Mode: shekelv1alpha1.ScopeModePerGroup, GroupBy: "team"}

	// Pre-create group CM with paused=false.
	existingGroupCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shekel-budget-grp-update-teamA",
			Namespace: "default",
			Labels: map[string]string{
				"shekel.dev/managed-by": "shekel-controller",
				"shekel.dev/budget":     "grp-update",
				labelGroup:              "teamA",
				labelCMType:             cmTypeBudgetGroup,
			},
		},
		Data: map[string]string{"budget_name": "grp-update", "group": "teamA", "paused": "false"},
	}
	// Spend report that pushes groupA over limit
	rA := makeSpendReport("pod-a", "grp-update", "default", "150.0", 5*time.Second, "teamA")
	r := reconciler(s, budget, existingGroupCM, rA)

	_, err := r.Reconcile(ctx, req("grp-update", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	// Group CM should now be paused=true (update path exercised).
	cm := &corev1.ConfigMap{}
	g.Expect(r.Get(ctx, types.NamespacedName{Name: "shekel-budget-grp-update-teamA", Namespace: "default"}, cm)).To(Succeed())
	g.Expect(cm.Data["paused"]).To(Equal("true"))
}

func TestSpendReportToShekelBudget(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	r := &ShekelBudgetReconciler{}

	// CM with budget label → produces request
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shekel-spend-pod-1",
			Namespace: "prod",
			Labels:    map[string]string{labelBudget: "my-budget"},
		},
	}
	requests := r.spendReportToShekelBudget(context.Background(), cm)
	g.Expect(requests).To(HaveLen(1))
	g.Expect(requests[0].Name).To(Equal("my-budget"))
	g.Expect(requests[0].Namespace).To(Equal("prod"))

	// CM without budget label → empty
	cm2 := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "prod"}}
	g.Expect(r.spendReportToShekelBudget(context.Background(), cm2)).To(BeEmpty())
}

func TestIsSpendReport(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)

	g.Expect(isSpendReport(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{labelSpendReport: "true"}},
	})).To(BeTrue())

	g.Expect(isSpendReport(&corev1.ConfigMap{})).To(BeFalse())

	g.Expect(isSpendReport(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{labelSpendReport: "false"}},
	})).To(BeFalse())
}

func TestEnsureRole_GetError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("role-err", "default")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(budget).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*rbacv1.Role); ok {
					return fmt.Errorf("role store error")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), req("role-err", "default"))
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("role store error"))
}

func TestEnsureRoleBinding_GetError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("rb-err", "default")

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(budget).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*rbacv1.RoleBinding); ok {
					return fmt.Errorf("rolebinding store error")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), req("rb-err", "default"))
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("rolebinding store error"))
}

func TestReconcileGroups_GetError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("grp-get-err", "default")
	budget.Spec.Scope = shekelv1alpha1.ScopeSpec{Mode: shekelv1alpha1.ScopeModePerGroup, GroupBy: "team"}
	rpt := makeSpendReport("pod-a", "grp-get-err", "default", "10.0", 5*time.Second, "teamA")

	calls := 0
	c := fake.NewClientBuilder().
		WithScheme(s).WithObjects(budget, rpt).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if cm, ok := obj.(*corev1.ConfigMap); ok && cm != nil {
					if key.Name == "shekel-budget-grp-get-err-teamA" {
						calls++
						if calls == 1 {
							return fmt.Errorf("group CM store error")
						}
					}
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), req("grp-get-err", "default"))
	g.Expect(err).To(HaveOccurred())
}

func TestReconcileGroups_CreateError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("grp-create-err", "default")
	budget.Spec.Scope = shekelv1alpha1.ScopeSpec{Mode: shekelv1alpha1.ScopeModePerGroup, GroupBy: "team"}
	rpt := makeSpendReport("pod-a", "grp-create-err", "default", "10.0", 5*time.Second, "teamA")

	c := fake.NewClientBuilder().
		WithScheme(s).WithObjects(budget, rpt).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Labels[labelCMType] == cmTypeBudgetGroup {
					return fmt.Errorf("group CM create error")
				}
				return cl.Create(ctx, obj, opts...)
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), req("grp-create-err", "default"))
	g.Expect(err).To(HaveOccurred())
}

func TestReconcileGroups_UpdateError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	ctx := context.Background()

	budget := minimalBudget("grp-upd-err", "default")
	budget.Spec.Scope = shekelv1alpha1.ScopeSpec{Mode: shekelv1alpha1.ScopeModePerGroup, GroupBy: "team"}
	existingGroupCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "shekel-budget-grp-upd-err-teamA", Namespace: "default",
			Labels: map[string]string{
				"shekel.dev/managed-by": "shekel-controller",
				"shekel.dev/budget": "grp-upd-err", labelGroup: "teamA", labelCMType: cmTypeBudgetGroup,
			},
		},
		Data: map[string]string{"paused": "false"},
	}
	rpt := makeSpendReport("pod-a", "grp-upd-err", "default", "200.0", 5*time.Second, "teamA")

	c := fake.NewClientBuilder().
		WithScheme(s).WithObjects(budget, existingGroupCM, rpt).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Labels[labelCMType] == cmTypeBudgetGroup {
					return fmt.Errorf("group CM update error")
				}
				return cl.Update(ctx, obj, opts...)
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(ctx, req("grp-upd-err", "default"))
	g.Expect(err).To(HaveOccurred())
}

func TestResetPeriod_DeleteError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("del-err", "default")
	budget.Spec.Period = shekelv1alpha1.PeriodDaily
	past := metav1.NewTime(time.Now().Add(-25 * time.Hour))
	budget.Status.PeriodStart = &past
	rpt := makeSpendReport("pod-a", "del-err", "default", "10.0", 5*time.Second, "")

	c := fake.NewClientBuilder().
		WithScheme(s).WithObjects(budget, rpt).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if _, ok := obj.(*corev1.ConfigMap); ok {
					return fmt.Errorf("delete spend report error")
				}
				return cl.Delete(ctx, obj, opts...)
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), req("del-err", "default"))
	g.Expect(err).To(HaveOccurred())
}

func TestReconcile_PeriodReset_BudgetCMAlreadyUnpaused(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	ctx := context.Background()

	budget := minimalBudget("reset-unpaused", "default")
	budget.Spec.Period = shekelv1alpha1.PeriodDaily
	past := metav1.NewTime(time.Now().Add(-25 * time.Hour))
	budget.Status.PeriodStart = &past
	// Budget CM already has paused=false — reset should not double-update
	r := reconciler(s, budget)

	_, err := r.Reconcile(ctx, req("reset-unpaused", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	updated := &shekelv1alpha1.ShekelBudget{}
	g.Expect(r.Get(ctx, types.NamespacedName{Name: "reset-unpaused", Namespace: "default"}, updated)).To(Succeed())
	g.Expect(*updated.Status.TotalSpent).To(BeZero())
}

func TestResetPeriod_GroupCMUpdateError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	ctx := context.Background()

	budget := minimalBudget("grp-upd-rst-err", "default")
	budget.Spec.Period = shekelv1alpha1.PeriodDaily
	past := metav1.NewTime(time.Now().Add(-25 * time.Hour))
	budget.Status.PeriodStart = &past

	// Pre-create a paused group Budget ConfigMap.
	groupCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "shekel-budget-grp-upd-rst-err-teamA", Namespace: "default",
			Labels: map[string]string{labelBudget: "grp-upd-rst-err", labelCMType: cmTypeBudgetGroup, labelGroup: "teamA"},
		},
		Data: map[string]string{"paused": "true"},
	}

	c := fake.NewClientBuilder().
		WithScheme(s).WithObjects(budget, groupCM).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Labels[labelCMType] == cmTypeBudgetGroup {
					return fmt.Errorf("group CM reset update error")
				}
				return cl.Update(ctx, obj, opts...)
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(ctx, req("grp-upd-rst-err", "default"))
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("group CM reset update error"))
}

func TestReconcile_SpendReportListError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("list-err", "default")

	c := fake.NewClientBuilder().
		WithScheme(s).WithObjects(budget).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*corev1.ConfigMapList); ok {
					return fmt.Errorf("spend report list error")
				}
				return cl.List(ctx, list, opts...)
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), req("list-err", "default"))
	g.Expect(err).To(HaveOccurred())
}

func TestReconcileGroups_NilData(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	ctx := context.Background()

	budget := minimalBudget("grp-nil", "default")
	budget.Spec.Scope = shekelv1alpha1.ScopeSpec{Mode: shekelv1alpha1.ScopeModePerGroup, GroupBy: "team"}

	// Group CM with nil Data (edge case: manually created with no data).
	nilDataCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shekel-budget-grp-nil-groupA",
			Namespace: "default",
			Labels: map[string]string{
				"shekel.dev/budget": "grp-nil", labelGroup: "groupA", labelCMType: cmTypeBudgetGroup,
				"shekel.dev/managed-by": "shekel-controller",
			},
		},
		// Data intentionally nil
	}
	rpt := makeSpendReport("pod-a", "grp-nil", "default", "10.0", 5*time.Second, "groupA") // spend < limit
	r := reconciler(s, budget, nilDataCM, rpt)

	_, err := r.Reconcile(ctx, req("grp-nil", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	// CM should have paused=false set (nil Data branch was exercised).
	cm := &corev1.ConfigMap{}
	g.Expect(r.Get(ctx, types.NamespacedName{Name: "shekel-budget-grp-nil-groupA", Namespace: "default"}, cm)).To(Succeed())
	g.Expect(cm.Data["paused"]).To(Equal("false"))
}

func TestResetPeriod_GroupCMListError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("rst-grp-list-err", "default")
	budget.Spec.Period = shekelv1alpha1.PeriodDaily
	past := metav1.NewTime(time.Now().Add(-25 * time.Hour))
	budget.Status.PeriodStart = &past

	calls := 0
	c := fake.NewClientBuilder().
		WithScheme(s).WithObjects(budget).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*corev1.ConfigMapList); ok {
					calls++
					if calls == 2 { // fail the group CM list (second ConfigMapList call)
						return fmt.Errorf("group CM list error")
					}
				}
				return cl.List(ctx, list, opts...)
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), req("rst-grp-list-err", "default"))
	g.Expect(err).To(HaveOccurred())
}

func TestReconcile_PeriodStartPatchError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("ps-patch-err", "default")

	c := fake.NewClientBuilder().
		WithScheme(s).WithObjects(budget).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, cl client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				return fmt.Errorf("status patch error")
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), req("ps-patch-err", "default"))
	g.Expect(err).To(HaveOccurred())
}

func TestReconcile_FinalStatusPatchError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("final-patch-err", "default")
	// Pre-set periodStart so it's not nil (skips the init patch, goes to final patch).
	now := metav1.Now()
	budget.Status.PeriodStart = &now

	patchCalls := 0
	c := fake.NewClientBuilder().
		WithScheme(s).WithObjects(budget).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, cl client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				patchCalls++
				// Fail the second status patch (the final aggregation/status update).
				if patchCalls >= 2 {
					return fmt.Errorf("final status patch error")
				}
				return cl.Status().Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), req("final-patch-err", "default"))
	g.Expect(err).To(HaveOccurred())
}

func TestResetPeriod_StatusPatchError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("rst-patch-err", "default")
	budget.Spec.Period = shekelv1alpha1.PeriodDaily
	past := metav1.NewTime(time.Now().Add(-25 * time.Hour))
	budget.Status.PeriodStart = &past

	patchCalls := 0
	c := fake.NewClientBuilder().
		WithScheme(s).WithObjects(budget).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, cl client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				patchCalls++
				if patchCalls >= 2 { // fail the resetPeriod status patch
					return fmt.Errorf("reset status patch error")
				}
				return cl.Status().Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), req("rst-patch-err", "default"))
	g.Expect(err).To(HaveOccurred())
}

func TestResetPeriod_ReportListError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("rst-list-err", "default")
	budget.Spec.Period = shekelv1alpha1.PeriodDaily
	past := metav1.NewTime(time.Now().Add(-25 * time.Hour))
	budget.Status.PeriodStart = &past

	c := fake.NewClientBuilder().
		WithScheme(s).WithObjects(budget).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*corev1.ConfigMapList); ok {
					return fmt.Errorf("reset list error")
				}
				return cl.List(ctx, list, opts...)
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), req("rst-list-err", "default"))
	g.Expect(err).To(HaveOccurred())
}

// ── mapsEqual unit tests ──────────────────────────────────────────────────────

// TestReconcile_PeriodStartPatch_SecondCallError covers the error path at the
// PeriodStart status patch (line ~95): first SubResourcePatch (setReconciled after
// CM create) succeeds; the second one (PeriodStart init) fails.
func TestReconcile_PeriodStartPatch_SecondCallError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("ps-patch-2nd-err", "default")

	patchCalls := 0
	c := fake.NewClientBuilder().
		WithScheme(s).WithObjects(budget).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, cl client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				patchCalls++
				if patchCalls >= 2 {
					return fmt.Errorf("period start patch error")
				}
				return cl.Status().Patch(ctx, obj, patch, opts...)
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), req("ps-patch-2nd-err", "default"))
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("period start patch error"))
}

// TestReconcile_SetReconciledAfterCMUpdateError covers the error path where the
// ConfigMap is updated (data drift) but the subsequent setReconciled status patch fails.
func TestReconcile_SetReconciledAfterCMUpdateError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("cm-upd-reconciled-err", "default")
	staleCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "shekel-budget-cm-upd-reconciled-err", Namespace: "default",
			Labels: map[string]string{
				"shekel.dev/managed-by": "shekel-controller",
				"shekel.dev/budget":     "cm-upd-reconciled-err",
			},
		},
		Data: map[string]string{
			"budget_name": "cm-upd-reconciled-err", "budget_namespace": "default",
			"max_usd": "999.0000", "period": "none", "scope_mode": "shared",
			"backend": "k8s", "paused": "false",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(s).WithObjects(budget, staleCM).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, cl client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				return fmt.Errorf("reconciled after update patch error")
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), req("cm-upd-reconciled-err", "default"))
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("reconciled after update patch error"))
}

// TestReconcile_KillSwitch_ActivationUpdateError covers the error path where the
// kill-switch CM update (setting paused=true) fails.
func TestReconcile_KillSwitch_ActivationUpdateError(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	budget := minimalBudget("ks-act-upd-err", "default") // maxUsd=100
	now := metav1.Now()
	budget.Status.PeriodStart = &now
	report := makeSpendReport("pod-a", "ks-act-upd-err", "default", "150.0", 5*time.Second, "")

	c := fake.NewClientBuilder().
		WithScheme(s).WithObjects(budget, report).
		WithStatusSubresource(&shekelv1alpha1.ShekelBudget{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Data != nil && cm.Data["paused"] == "true" {
					return fmt.Errorf("kill-switch activation update error")
				}
				return cl.Update(ctx, obj, opts...)
			},
		}).Build()
	r := &ShekelBudgetReconciler{Client: c, Scheme: s}

	_, err := r.Reconcile(context.Background(), req("ks-act-upd-err", "default"))
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("kill-switch activation update error"))
}

// TestResetPeriod_GroupCMNilData covers the cm.Data==nil defensive branch in
// resetPeriod when a group CM exists with no data (nil map).
func TestResetPeriod_GroupCMNilData(t *testing.T) {
	t.Parallel()
	g := NewWithT(t)
	s := testScheme(t)
	ctx := context.Background()

	budget := minimalBudget("rst-nil-data", "default")
	budget.Spec.Period = shekelv1alpha1.PeriodDaily
	past := metav1.NewTime(time.Now().Add(-25 * time.Hour))
	budget.Status.PeriodStart = &past

	nilDataGroupCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "shekel-budget-rst-nil-data-teamA", Namespace: "default",
			Labels: map[string]string{
				labelBudget: "rst-nil-data",
				labelCMType: cmTypeBudgetGroup,
			},
		},
	}
	r := reconciler(s, budget, nilDataGroupCM)

	_, err := r.Reconcile(ctx, req("rst-nil-data", "default"))
	g.Expect(err).NotTo(HaveOccurred())

	updated := &corev1.ConfigMap{}
	g.Expect(r.Get(ctx, types.NamespacedName{Name: "shekel-budget-rst-nil-data-teamA", Namespace: "default"}, updated)).To(Succeed())
	g.Expect(updated.Data["paused"]).To(Equal("false"))
}

func TestMapsEqual(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		a, b     map[string]string
		expected bool
	}{
		{"equal", map[string]string{"k": "v"}, map[string]string{"k": "v"}, true},
		{"different value", map[string]string{"k": "v1"}, map[string]string{"k": "v2"}, false},
		{"extra key in b", map[string]string{"k": "v"}, map[string]string{"k": "v", "x": "y"}, false},
		{"extra key in a", map[string]string{"k": "v", "x": "y"}, map[string]string{"k": "v"}, false},
		{"both empty", map[string]string{}, map[string]string{}, true},
		{"both nil", nil, nil, true},
		{"nil vs empty", nil, map[string]string{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(mapsEqual(tt.a, tt.b)).To(Equal(tt.expected))
		})
	}
}
