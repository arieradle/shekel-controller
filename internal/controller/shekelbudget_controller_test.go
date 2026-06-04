// Copyright (c) 2026 Arie Radle. MIT License.

package controller

import (
	"context"
	"fmt"
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
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
	g.Expect(shekelv1alpha1.AddToScheme(s)).To(Succeed())
	return s
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

// ── mapsEqual unit tests ──────────────────────────────────────────────────────

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
