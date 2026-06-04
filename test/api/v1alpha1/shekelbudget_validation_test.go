// Copyright (c) 2026 Arie Radle. MIT License.

package v1alpha1_test

import (
	"context"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	shekelv1alpha1 "github.com/arieradle/shekel-controller/api/v1alpha1"
)

var (
	cfg       *rest.Config
	k8sClient client.Client
	testEnv   *envtest.Environment
	scheme    = runtime.NewScheme()
)

func TestMain(m *testing.M) {
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "..", "config", "crd", "bases"),
		},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	cfg, err = testEnv.Start()
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := testEnv.Stop(); err != nil {
			panic(err)
		}
	}()

	if err := shekelv1alpha1.AddToScheme(scheme); err != nil {
		panic(err)
	}

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		panic(err)
	}

	m.Run()
}

func budget(name string, maxUsd float64, mutate func(*shekelv1alpha1.ShekelBudget)) *shekelv1alpha1.ShekelBudget {
	b := &shekelv1alpha1.ShekelBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: shekelv1alpha1.ShekelBudgetSpec{
			MaxUsd: maxUsd,
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
		},
	}
	if mutate != nil {
		mutate(b)
	}
	return b
}

func TestMaxUsd(t *testing.T) {
	ctx := context.Background()

	t.Run("zero is rejected", func(t *testing.T) {
		g := NewWithT(t)
		b := budget("maxusd-zero", 0, nil)
		err := k8sClient.Create(ctx, b)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("maxUsd must be greater than 0"))
	})

	t.Run("negative is rejected", func(t *testing.T) {
		g := NewWithT(t)
		b := budget("maxusd-neg", -1, nil)
		err := k8sClient.Create(ctx, b)
		g.Expect(err).To(HaveOccurred())
	})

	t.Run("positive is accepted", func(t *testing.T) {
		g := NewWithT(t)
		b := budget("maxusd-ok", 10, nil)
		g.Expect(k8sClient.Create(ctx, b)).To(Succeed())
		g.Expect(k8sClient.Delete(ctx, b)).To(Succeed())
	})
}

func TestWarnAt(t *testing.T) {
	ctx := context.Background()

	t.Run("above 1 is rejected", func(t *testing.T) {
		g := NewWithT(t)
		warnAt := 1.5
		b := budget("warnat-high", 10, func(b *shekelv1alpha1.ShekelBudget) {
			b.Spec.WarnAt = &warnAt
		})
		err := k8sClient.Create(ctx, b)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("warnAt must be between 0 and 1"))
	})

	t.Run("negative is rejected", func(t *testing.T) {
		g := NewWithT(t)
		warnAt := -0.1
		b := budget("warnat-neg", 10, func(b *shekelv1alpha1.ShekelBudget) {
			b.Spec.WarnAt = &warnAt
		})
		err := k8sClient.Create(ctx, b)
		g.Expect(err).To(HaveOccurred())
	})

	t.Run("valid fraction is accepted", func(t *testing.T) {
		g := NewWithT(t)
		warnAt := 0.8
		b := budget("warnat-ok", 10, func(b *shekelv1alpha1.ShekelBudget) {
			b.Spec.WarnAt = &warnAt
		})
		g.Expect(k8sClient.Create(ctx, b)).To(Succeed())
		g.Expect(k8sClient.Delete(ctx, b)).To(Succeed())
	})
}

func TestScopePerGroup(t *testing.T) {
	ctx := context.Background()

	t.Run("per-group without groupBy is rejected", func(t *testing.T) {
		g := NewWithT(t)
		b := budget("pergroup-nogroupby", 10, func(b *shekelv1alpha1.ShekelBudget) {
			b.Spec.Scope = shekelv1alpha1.ScopeSpec{Mode: shekelv1alpha1.ScopeModePerGroup}
		})
		err := k8sClient.Create(ctx, b)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("groupBy is required when scope.mode is per-group"))
	})

	t.Run("per-group with empty groupBy is rejected", func(t *testing.T) {
		g := NewWithT(t)
		b := budget("pergroup-emptygroupby", 10, func(b *shekelv1alpha1.ShekelBudget) {
			b.Spec.Scope = shekelv1alpha1.ScopeSpec{Mode: shekelv1alpha1.ScopeModePerGroup, GroupBy: ""}
		})
		err := k8sClient.Create(ctx, b)
		g.Expect(err).To(HaveOccurred())
	})

	t.Run("per-group with groupBy is accepted", func(t *testing.T) {
		g := NewWithT(t)
		b := budget("pergroup-ok", 10, func(b *shekelv1alpha1.ShekelBudget) {
			b.Spec.Scope = shekelv1alpha1.ScopeSpec{Mode: shekelv1alpha1.ScopeModePerGroup, GroupBy: "tenant-id"}
		})
		g.Expect(k8sClient.Create(ctx, b)).To(Succeed())
		g.Expect(k8sClient.Delete(ctx, b)).To(Succeed())
	})

	t.Run("shared without groupBy is accepted", func(t *testing.T) {
		g := NewWithT(t)
		b := budget("shared-ok", 10, func(b *shekelv1alpha1.ShekelBudget) {
			b.Spec.Scope = shekelv1alpha1.ScopeSpec{Mode: shekelv1alpha1.ScopeModeShared}
		})
		g.Expect(k8sClient.Create(ctx, b)).To(Succeed())
		g.Expect(k8sClient.Delete(ctx, b)).To(Succeed())
	})
}

func TestEnforcement(t *testing.T) {
	ctx := context.Background()

	t.Run("flushEveryUsd zero is rejected", func(t *testing.T) {
		g := NewWithT(t)
		zero := float64(0)
		b := budget("flush-usd-zero", 10, func(b *shekelv1alpha1.ShekelBudget) {
			b.Spec.Enforcement = shekelv1alpha1.EnforcementSpec{FlushEveryUsd: &zero}
		})
		err := k8sClient.Create(ctx, b)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("flushEveryUsd must be greater than 0"))
	})

	t.Run("flushEverySeconds negative is rejected", func(t *testing.T) {
		g := NewWithT(t)
		neg := int32(-1)
		b := budget("flush-sec-neg", 10, func(b *shekelv1alpha1.ShekelBudget) {
			b.Spec.Enforcement = shekelv1alpha1.EnforcementSpec{FlushEverySeconds: &neg}
		})
		err := k8sClient.Create(ctx, b)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("flushEverySeconds must be greater than 0"))
	})

	t.Run("valid enforcement config is accepted", func(t *testing.T) {
		g := NewWithT(t)
		usd := float64(1)
		secs := int32(30)
		b := budget("enforcement-ok", 10, func(b *shekelv1alpha1.ShekelBudget) {
			b.Spec.Enforcement = shekelv1alpha1.EnforcementSpec{
				Backend:           shekelv1alpha1.BackendK8s,
				FlushEveryUsd:     &usd,
				FlushEverySeconds: &secs,
			}
		})
		g.Expect(k8sClient.Create(ctx, b)).To(Succeed())
		g.Expect(k8sClient.Delete(ctx, b)).To(Succeed())
	})
}

func TestPeriodEnum(t *testing.T) {
	ctx := context.Background()

	t.Run("invalid period is rejected", func(t *testing.T) {
		g := NewWithT(t)
		b := budget("period-invalid", 10, func(b *shekelv1alpha1.ShekelBudget) {
			b.Spec.Period = "quarterly"
		})
		err := k8sClient.Create(ctx, b)
		g.Expect(err).To(HaveOccurred())
	})

	t.Run("valid periods are accepted", func(t *testing.T) {
		ctx := context.Background()
		for _, p := range []shekelv1alpha1.PeriodType{
			shekelv1alpha1.PeriodMonthly,
			shekelv1alpha1.PeriodDaily,
			shekelv1alpha1.PeriodRolling24h,
			shekelv1alpha1.PeriodNone,
		} {
			t.Run(string(p), func(t *testing.T) {
				g := NewWithT(t)
				name := "period-" + string(p)
				b := budget(name, 10, func(b *shekelv1alpha1.ShekelBudget) {
					b.Spec.Period = p
				})
				g.Expect(k8sClient.Create(ctx, b)).To(Succeed())
				g.Expect(k8sClient.Delete(ctx, b)).To(Succeed())
			})
		}
	})
}

func TestRedisSpec(t *testing.T) {
	ctx := context.Background()

	t.Run("redis backend without redis spec is rejected", func(t *testing.T) {
		g := NewWithT(t)
		b := budget("redis-no-spec", 10, func(b *shekelv1alpha1.ShekelBudget) {
			b.Spec.Enforcement = shekelv1alpha1.EnforcementSpec{Backend: shekelv1alpha1.BackendRedis}
		})
		err := k8sClient.Create(ctx, b)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("redis spec is required"))
	})

	t.Run("redis backend with redis spec is accepted", func(t *testing.T) {
		g := NewWithT(t)
		b := budget("redis-with-spec", 10, func(b *shekelv1alpha1.ShekelBudget) {
			b.Spec.Enforcement = shekelv1alpha1.EnforcementSpec{Backend: shekelv1alpha1.BackendRedis}
			b.Spec.Redis = &shekelv1alpha1.RedisSpec{
				SecretRef: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "redis-creds"},
					Key:                  "REDIS_URL",
				},
			}
		})
		g.Expect(k8sClient.Create(ctx, b)).To(Succeed())
		g.Expect(k8sClient.Delete(ctx, b)).To(Succeed())
	})

	t.Run("per-pod scope with redis backend is rejected", func(t *testing.T) {
		g := NewWithT(t)
		b := budget("redis-per-pod", 10, func(b *shekelv1alpha1.ShekelBudget) {
			b.Spec.Scope = shekelv1alpha1.ScopeSpec{Mode: shekelv1alpha1.ScopeModePerPod}
			b.Spec.Enforcement = shekelv1alpha1.EnforcementSpec{Backend: shekelv1alpha1.BackendRedis}
			b.Spec.Redis = &shekelv1alpha1.RedisSpec{
				SecretRef: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "redis-creds"},
					Key:                  "REDIS_URL",
				},
			}
		})
		err := k8sClient.Create(ctx, b)
		g.Expect(err).To(HaveOccurred())
		g.Expect(err.Error()).To(ContainSubstring("redis backend is not supported with per-pod scope"))
	})
}

func TestMaxLLMCalls(t *testing.T) {
	ctx := context.Background()

	t.Run("maxLLMCalls is accepted", func(t *testing.T) {
		g := NewWithT(t)
		calls := int32(500)
		b := budget("max-calls-ok", 10, func(b *shekelv1alpha1.ShekelBudget) {
			b.Spec.MaxLLMCalls = &calls
		})
		g.Expect(k8sClient.Create(ctx, b)).To(Succeed())
		g.Expect(k8sClient.Delete(ctx, b)).To(Succeed())
	})
}
