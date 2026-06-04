package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PeriodType defines how the budget window resets.
// +kubebuilder:validation:Enum=monthly;daily;rolling-24h;none
type PeriodType string

const (
	PeriodMonthly    PeriodType = "monthly"
	PeriodDaily      PeriodType = "daily"
	PeriodRolling24h PeriodType = "rolling-24h"
	PeriodNone       PeriodType = "none"
)

// ScopeMode controls how spend is tracked across matching pods.
// +kubebuilder:validation:Enum=shared;per-pod;per-group
type ScopeMode string

const (
	ScopeModeShared   ScopeMode = "shared"
	ScopeModePerPod   ScopeMode = "per-pod"
	ScopeModePerGroup ScopeMode = "per-group"
)

// BackendType selects the enforcement backend.
// +kubebuilder:validation:Enum=k8s;redis
type BackendType string

const (
	BackendK8s   BackendType = "k8s"
	BackendRedis BackendType = "redis"
)

// FallbackSpec instructs the shekel library to switch models as spend approaches the limit.
type FallbackSpec struct {
	// Model is the model identifier to switch to (e.g. "gpt-4o-mini").
	// +kubebuilder:validation:MinLength=1
	Model string `json:"model"`

	// AtPct is the fraction of maxUsd at which the fallback activates (e.g. 0.9 = 90%).
	AtPct float64 `json:"atPct"`
}

// ScopeSpec controls how the budget is partitioned across pods.
type ScopeSpec struct {
	// Mode determines whether pods share a single budget, each have their own, or are grouped by label.
	// Defaults to shared.
	// +kubebuilder:default=shared
	Mode ScopeMode `json:"mode,omitempty"`

	// GroupBy is the pod label key used to partition spend in per-group mode.
	// Required when mode is per-group.
	// +optional
	GroupBy string `json:"groupBy,omitempty"`

	// PerPodCap is an optional secondary USD ceiling per individual pod.
	// Applies in addition to the shared/group limit.
	// +optional
	PerPodCap *float64 `json:"perPodCap,omitempty"`
}

// RedisSpec configures the Redis enforcement backend.
type RedisSpec struct {
	// SecretRef references a Secret key whose value is the Redis connection URL.
	// Supported formats: redis://host:port, rediss://host:port (TLS), redis://:password@host:port
	SecretRef corev1.SecretKeySelector `json:"secretRef"`

	// OnUnavailable controls behaviour when Redis is unreachable.
	// closed (default): raise BudgetExceededError. open: allow calls through.
	// +kubebuilder:default=closed
	// +kubebuilder:validation:Enum=closed;open
	OnUnavailable string `json:"onUnavailable,omitempty"`
}

// EnforcementSpec controls how and when spend is flushed and enforced.
type EnforcementSpec struct {
	// Backend selects the enforcement mechanism. k8s uses ConfigMap-based soft enforcement;
	// redis uses atomic Lua-script hard enforcement. Defaults to k8s.
	// +kubebuilder:default=k8s
	Backend BackendType `json:"backend,omitempty"`

	// FlushEveryUsd triggers a spend report to Kubernetes after this many USD have accumulated locally.
	// Must be > 0 if set.
	// +optional
	FlushEveryUsd *float64 `json:"flushEveryUsd,omitempty"`

	// FlushEverySeconds triggers a spend report to Kubernetes at this interval.
	// Must be > 0 if set.
	// +optional
	FlushEverySeconds *int32 `json:"flushEverySeconds,omitempty"`
}

// ShekelBudgetSpec defines the desired budget policy.
//
// +kubebuilder:validation:XValidation:rule="self.maxUsd > 0",message="maxUsd must be greater than 0"
// +kubebuilder:validation:XValidation:rule="!has(self.warnAt) || (self.warnAt >= 0.0 && self.warnAt <= 1.0)",message="warnAt must be between 0 and 1"
// +kubebuilder:validation:XValidation:rule="self.scope.mode != 'per-group' || (has(self.scope.groupBy) && self.scope.groupBy != '')",message="groupBy is required when scope.mode is per-group"
// +kubebuilder:validation:XValidation:rule="!has(self.enforcement.flushEveryUsd) || self.enforcement.flushEveryUsd > 0",message="flushEveryUsd must be greater than 0"
// +kubebuilder:validation:XValidation:rule="!has(self.enforcement.flushEverySeconds) || self.enforcement.flushEverySeconds > 0",message="flushEverySeconds must be greater than 0"
// +kubebuilder:validation:XValidation:rule="self.enforcement.backend != 'redis' || has(self.redis)",message="redis spec is required when enforcement.backend is redis"
// +kubebuilder:validation:XValidation:rule="self.scope.mode != 'per-pod' || self.enforcement.backend != 'redis'",message="redis backend is not supported with per-pod scope"
type ShekelBudgetSpec struct {
	// MaxUsd is the hard USD spending cap for the period. Required, must be > 0.
	MaxUsd float64 `json:"maxUsd"`

	// Period defines when the budget resets. Defaults to none (no automatic reset).
	// +kubebuilder:default=none
	Period PeriodType `json:"period,omitempty"`

	// WarnAt is the fraction of maxUsd at which a BudgetWarning condition is raised (e.g. 0.8 = 80%).
	// Must be in [0, 1]. Defaults to 0.8.
	// +optional
	WarnAt *float64 `json:"warnAt,omitempty"`

	// Fallback configures automatic model switching as spend approaches the limit.
	// +optional
	Fallback *FallbackSpec `json:"fallback,omitempty"`

	// Scope controls how spend is partitioned across matching pods.
	// +optional
	Scope ScopeSpec `json:"scope,omitempty"`

	// Enforcement controls the backend and flush thresholds for spend reporting.
	// +optional
	Enforcement EnforcementSpec `json:"enforcement,omitempty"`

	// MaxLLMCalls is a hard cap on the number of LLM API calls per period.
	// +optional
	MaxLLMCalls *int32 `json:"maxLLMCalls,omitempty"`

	// Redis configures the Redis enforcement backend.
	// Required when enforcement.backend is redis.
	// +optional
	Redis *RedisSpec `json:"redis,omitempty"`

	// Selector identifies which pods this budget applies to.
	Selector metav1.LabelSelector `json:"selector"`
}

// GroupStatus reports per-group spend in per-group scope mode.
type GroupStatus struct {
	// Key is the value of the groupBy label for this group.
	Key string `json:"key"`

	// Spent is the aggregated USD spend for this group in the current period.
	Spent float64 `json:"spent"`
}

// ShekelBudgetStatus is written by the controller and reflects observed spend state.
type ShekelBudgetStatus struct {
	// TotalSpent is the aggregated USD spent by all participating pods in the current period.
	// +optional
	TotalSpent *float64 `json:"totalSpent,omitempty"`

	// ParticipatingPods is the number of pods currently reporting spend.
	// +optional
	ParticipatingPods int32 `json:"participatingPods,omitempty"`

	// PeriodStart is the beginning of the current budget period.
	// +optional
	PeriodStart *metav1.Time `json:"periodStart,omitempty"`

	// LastFlush is the timestamp of the most recent spend report received from any pod.
	// +optional
	LastFlush *metav1.Time `json:"lastFlush,omitempty"`

	// Conditions reflect the current state of the budget.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Groups holds per-group spend breakdowns. Populated only when scope.mode=per-group.
	// +optional
	Groups []GroupStatus `json:"groups,omitempty"`
}

// ShekelBudget declares an LLM spend budget for a group of pods.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=sb,categories=shekel
// +kubebuilder:printcolumn:name="Limit",type=string,JSONPath=`.spec.maxUsd`
// +kubebuilder:printcolumn:name="Spent",type=string,JSONPath=`.status.totalSpent`
// +kubebuilder:printcolumn:name="Used%",type=string,JSONPath=`.status.conditions[?(@.type=="BudgetWarning")].status`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.conditions[?(@.type=="BudgetPaused")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ShekelBudget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ShekelBudgetSpec   `json:"spec,omitempty"`
	Status ShekelBudgetStatus `json:"status,omitempty"`
}

// ShekelBudgetList contains a list of ShekelBudget.
// +kubebuilder:object:root=true
type ShekelBudgetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ShekelBudget `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ShekelBudget{}, &ShekelBudgetList{})
}
