package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AutoApplySpec defines the desired state of AutoApply
type AutoApplySpec struct {
	// ConfigMapRef references the ConfigMap containing manifests to apply
	// +kubebuilder:validation:Required
	ConfigMapRef ConfigMapReference `json:"configMapRef"`

	// Prune enables garbage collection of resources removed from the ConfigMap
	// +kubebuilder:default:=false
	Prune bool `json:"prune,omitempty"`
}

// ConfigMapReference contains reference to a ConfigMap
type ConfigMapReference struct {
	// Name is the ConfigMap name
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Namespace is the ConfigMap namespace (defaults to AutoApply's namespace)
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// AutoApplyStatus defines the observed state of AutoApply
type AutoApplyStatus struct {
	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// AppliedResources lists the resources currently managed by this AutoApply
	// +optional
	AppliedResources []ResourceReference `json:"appliedResources,omitempty"`

	// LastAppliedConfigMapResourceVersion tracks the ConfigMap version last applied
	// +optional
	LastAppliedConfigMapResourceVersion string `json:"lastAppliedConfigMapResourceVersion,omitempty"`

	// ObservedGeneration is the last observed generation
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// ResourceReference identifies an applied resource
type ResourceReference struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="ConfigMap",type="string",JSONPath=".spec.configMapRef.name"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// AutoApply is the Schema for the autoapplies API
type AutoApply struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AutoApplySpec   `json:"spec,omitempty"`
	Status AutoApplyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AutoApplyList contains a list of AutoApply
type AutoApplyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AutoApply `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AutoApply{}, &AutoApplyList{})
}
