package controller

import (
	"context"
	"regexp"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	autoapplyv1alpha1 "github.com/charlie/k8s-autoapply-operator/api/v1alpha1"
)

// ConfigMapReconciler watches ConfigMaps and restarts pods that use them
type ConfigMapReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// configMapVersions tracks the last seen ResourceVersion for each ConfigMap
	configMapVersions sync.Map
}

// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=autoapply.io,resources=autoapplyconfigs,verbs=get;list;watch

func (r *ConfigMapReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the ConfigMap
	var configMap corev1.ConfigMap
	if err := r.Get(ctx, req.NamespacedName, &configMap); err != nil {
		// ConfigMap deleted, clean up tracking
		r.configMapVersions.Delete(req.String())
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if this is an update (not first time seeing it)
	key := req.String()
	lastVersion, seen := r.configMapVersions.Load(key)
	r.configMapVersions.Store(key, configMap.ResourceVersion)

	if !seen {
		// First time seeing this ConfigMap, just track it
		logger.V(1).Info("Tracking ConfigMap", "configmap", req.NamespacedName)
		return ctrl.Result{}, nil
	}

	if lastVersion == configMap.ResourceVersion {
		// No change
		return ctrl.Result{}, nil
	}

	logger.Info("ConfigMap changed, finding affected pods", "configmap", req.NamespacedName)

	// Load exclusion config
	excludePatterns, excludeNamespaces := r.loadExclusionConfig(ctx)

	// Skip if namespace is excluded
	for _, ns := range excludeNamespaces {
		if ns == configMap.Namespace {
			logger.Info("Namespace excluded, skipping", "namespace", configMap.Namespace)
			return ctrl.Result{}, nil
		}
	}

	// Find pods in the same namespace that use this ConfigMap
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(configMap.Namespace)); err != nil {
		logger.Error(err, "Failed to list pods")
		return ctrl.Result{}, err
	}

	for _, pod := range pods.Items {
		// Skip completed/failed pods
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}

		// Check if pod is excluded
		if r.isPodExcluded(pod.Name, excludePatterns) {
			logger.V(1).Info("Pod excluded by pattern", "pod", pod.Name)
			continue
		}

		// Check if pod uses this ConfigMap
		if r.podUsesConfigMap(&pod, configMap.Name) {
			logger.Info("Restarting pod due to ConfigMap change",
				"pod", pod.Name,
				"configmap", configMap.Name)

			if err := r.Delete(ctx, &pod); err != nil {
				logger.Error(err, "Failed to delete pod", "pod", pod.Name)
				// Continue with other pods
			}
		}
	}

	return ctrl.Result{}, nil
}

// podUsesConfigMap checks if a pod references the given ConfigMap
func (r *ConfigMapReconciler) podUsesConfigMap(pod *corev1.Pod, configMapName string) bool {
	// Check volumes
	for _, vol := range pod.Spec.Volumes {
		if vol.ConfigMap != nil && vol.ConfigMap.Name == configMapName {
			return true
		}
		if vol.Projected != nil {
			for _, src := range vol.Projected.Sources {
				if src.ConfigMap != nil && src.ConfigMap.Name == configMapName {
					return true
				}
			}
		}
	}

	// Check containers for envFrom
	for _, container := range pod.Spec.Containers {
		for _, envFrom := range container.EnvFrom {
			if envFrom.ConfigMapRef != nil && envFrom.ConfigMapRef.Name == configMapName {
				return true
			}
		}
		// Check individual env vars
		for _, env := range container.Env {
			if env.ValueFrom != nil && env.ValueFrom.ConfigMapKeyRef != nil {
				if env.ValueFrom.ConfigMapKeyRef.Name == configMapName {
					return true
				}
			}
		}
	}

	// Check init containers
	for _, container := range pod.Spec.InitContainers {
		for _, envFrom := range container.EnvFrom {
			if envFrom.ConfigMapRef != nil && envFrom.ConfigMapRef.Name == configMapName {
				return true
			}
		}
		for _, env := range container.Env {
			if env.ValueFrom != nil && env.ValueFrom.ConfigMapKeyRef != nil {
				if env.ValueFrom.ConfigMapKeyRef.Name == configMapName {
					return true
				}
			}
		}
	}

	return false
}

// loadExclusionConfig loads exclusion patterns from AutoApplyConfig
func (r *ConfigMapReconciler) loadExclusionConfig(ctx context.Context) (podPatterns []*regexp.Regexp, namespaces []string) {
	var configList autoapplyv1alpha1.AutoApplyConfigList
	if err := r.List(ctx, &configList); err != nil {
		return nil, nil
	}

	for _, cfg := range configList.Items {
		for _, pattern := range cfg.Spec.ExcludePods {
			if re, err := regexp.Compile(pattern); err == nil {
				podPatterns = append(podPatterns, re)
			}
		}
		namespaces = append(namespaces, cfg.Spec.ExcludeNamespaces...)
	}

	return podPatterns, namespaces
}

// isPodExcluded checks if pod name matches any exclusion pattern
func (r *ConfigMapReconciler) isPodExcluded(podName string, patterns []*regexp.Regexp) bool {
	for _, re := range patterns {
		if re.MatchString(podName) {
			return true
		}
	}
	return false
}

func (r *ConfigMapReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Give pods time to start before we begin tracking ConfigMaps
	// This prevents mass restarts on operator startup
	go func() {
		time.Sleep(10 * time.Second)
	}()

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}).
		Complete(r)
}

