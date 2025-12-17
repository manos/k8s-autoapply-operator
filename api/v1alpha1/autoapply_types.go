package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AutoApplyConfigSpec defines the configuration for the operator
type AutoApplyConfigSpec struct {
	// ExcludePods is a list of regex patterns for pod names to exclude from auto-restart
	// +optional
	ExcludePods []string `json:"excludePods,omitempty"`

	// ExcludeNamespaces is a list of namespaces to exclude from watching
	// +optional
	ExcludeNamespaces []string `json:"excludeNamespaces,omitempty"`

	// YoloMode disables safe rolling restarts - all pods restart at once
	// +optional
	YoloMode bool `json:"yoloMode,omitempty"`
}

// AutoApplyConfigStatus defines the observed state
type AutoApplyConfigStatus struct {
	// LastUpdated is when the config was last applied
	// +optional
	LastUpdated metav1.Time `json:"lastUpdated,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// AutoApplyConfig is the Schema for operator configuration
type AutoApplyConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AutoApplyConfigSpec   `json:"spec,omitempty"`
	Status AutoApplyConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AutoApplyConfigList contains a list of AutoApplyConfig
type AutoApplyConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AutoApplyConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AutoApplyConfig{}, &AutoApplyConfigList{})
}
