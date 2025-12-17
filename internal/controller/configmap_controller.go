package controller

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	autoapplyv1alpha1 "github.com/charlie/k8s-autoapply-operator/api/v1alpha1"
)

const (
	// Time to wait between restart batches
	batchWaitDuration = 5 * time.Second
	// Time to wait for pods to become ready
	podReadyTimeout = 60 * time.Second
	// Poll interval when waiting for pods
	pollInterval = 2 * time.Second
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
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch
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

	// Find pods that use this ConfigMap
	podsToRestart := r.findPodsUsingConfigMap(ctx, &configMap, excludePatterns)
	if len(podsToRestart) == 0 {
		logger.Info("No pods to restart")
		return ctrl.Result{}, nil
	}

	logger.Info("Found pods to restart", "count", len(podsToRestart))

	// Load PDBs for the namespace
	pdbs, err := r.loadPDBs(ctx, configMap.Namespace)
	if err != nil {
		logger.Error(err, "Failed to load PDBs, proceeding without PDB checks")
	}

	// Perform rolling restart: 50% -> wait -> check health -> 50%
	if err := r.rollingRestart(ctx, podsToRestart, pdbs); err != nil {
		logger.Error(err, "Rolling restart encountered errors")
		// Don't return error - we've done what we can
	}

	return ctrl.Result{}, nil
}

// findPodsUsingConfigMap returns pods that reference the given ConfigMap
func (r *ConfigMapReconciler) findPodsUsingConfigMap(ctx context.Context, configMap *corev1.ConfigMap, excludePatterns []*regexp.Regexp) []corev1.Pod {
	logger := log.FromContext(ctx)

	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(configMap.Namespace)); err != nil {
		logger.Error(err, "Failed to list pods")
		return nil
	}

	var result []corev1.Pod
	for _, pod := range pods.Items {
		// Skip completed/failed pods
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}

		// Skip pods being deleted
		if pod.DeletionTimestamp != nil {
			continue
		}

		// Check if pod is excluded
		if r.isPodExcluded(pod.Name, excludePatterns) {
			logger.V(1).Info("Pod excluded by pattern", "pod", pod.Name)
			continue
		}

		// Check if pod uses this ConfigMap
		if r.podUsesConfigMap(&pod, configMap.Name) {
			result = append(result, pod)
		}
	}

	return result
}

// rollingRestart performs a 50/50 rolling restart with health checks
func (r *ConfigMapReconciler) rollingRestart(ctx context.Context, pods []corev1.Pod, pdbs []policyv1.PodDisruptionBudget) error {
	logger := log.FromContext(ctx)

	if len(pods) == 0 {
		return nil
	}

	// Split into two batches
	midpoint := (len(pods) + 1) / 2 // Round up for first batch
	firstBatch := pods[:midpoint]
	secondBatch := pods[midpoint:]

	logger.Info("Starting rolling restart",
		"total", len(pods),
		"firstBatch", len(firstBatch),
		"secondBatch", len(secondBatch))

	// Restart first batch
	restartedPods := r.restartBatch(ctx, firstBatch, pdbs)
	if len(restartedPods) == 0 {
		logger.Info("No pods were restarted in first batch (PDB constraints)")
		return nil
	}

	// If there's a second batch, wait and check health before continuing
	if len(secondBatch) > 0 {
		logger.Info("Waiting before second batch", "duration", batchWaitDuration)
		time.Sleep(batchWaitDuration)

		// Wait for first batch pods to be replaced and healthy
		if err := r.waitForPodsHealthy(ctx, restartedPods); err != nil {
			logger.Error(err, "First batch pods not healthy, aborting second batch")
			return fmt.Errorf("first batch unhealthy: %w", err)
		}

		logger.Info("First batch healthy, restarting second batch")
		r.restartBatch(ctx, secondBatch, pdbs)
	}

	return nil
}

// restartBatch deletes pods in a batch, respecting PDBs
func (r *ConfigMapReconciler) restartBatch(ctx context.Context, pods []corev1.Pod, pdbs []policyv1.PodDisruptionBudget) []corev1.Pod {
	logger := log.FromContext(ctx)
	var restarted []corev1.Pod

	for _, pod := range pods {
		// Check PDB before deleting
		if !r.canDeletePod(ctx, &pod, pdbs) {
			logger.Info("Skipping pod due to PDB constraints", "pod", pod.Name)
			continue
		}

		logger.Info("Restarting pod", "pod", pod.Name)
		if err := r.Delete(ctx, &pod); err != nil {
			logger.Error(err, "Failed to delete pod", "pod", pod.Name)
			continue
		}
		restarted = append(restarted, pod)
	}

	return restarted
}

// canDeletePod checks if deleting a pod would violate any PDB
func (r *ConfigMapReconciler) canDeletePod(ctx context.Context, pod *corev1.Pod, pdbs []policyv1.PodDisruptionBudget) bool {
	logger := log.FromContext(ctx)

	for _, pdb := range pdbs {
		if pdb.Spec.Selector == nil {
			continue
		}

		// Check if PDB selects this pod
		selector, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil {
			continue
		}

		if !selector.Matches(labels.Set(pod.Labels)) {
			continue
		}

		// PDB applies to this pod - check if we can disrupt
		// DisruptionsAllowed tells us how many more disruptions are allowed
		if pdb.Status.DisruptionsAllowed <= 0 {
			logger.V(1).Info("PDB would be violated",
				"pdb", pdb.Name,
				"pod", pod.Name,
				"disruptionsAllowed", pdb.Status.DisruptionsAllowed)
			return false
		}

		// Also check minAvailable if set
		if pdb.Spec.MinAvailable != nil {
			currentHealthy := pdb.Status.CurrentHealthy
			minAvailable := getIntOrPercentValue(pdb.Spec.MinAvailable, int(pdb.Status.ExpectedPods))
			if currentHealthy-1 < int32(minAvailable) {
				logger.V(1).Info("PDB minAvailable would be violated",
					"pdb", pdb.Name,
					"pod", pod.Name,
					"currentHealthy", currentHealthy,
					"minAvailable", minAvailable)
				return false
			}
		}
	}

	return true
}

// waitForPodsHealthy waits for replacement pods to be ready
func (r *ConfigMapReconciler) waitForPodsHealthy(ctx context.Context, deletedPods []corev1.Pod) error {
	logger := log.FromContext(ctx)

	if len(deletedPods) == 0 {
		return nil
	}

	// We need to wait for the owning controllers to create new pods
	// and for those pods to become ready
	deadline := time.Now().Add(podReadyTimeout)

	for time.Now().Before(deadline) {
		allHealthy := true

		for _, oldPod := range deletedPods {
			// Find pods with the same owner
			healthy, err := r.checkOwnerPodsHealthy(ctx, &oldPod)
			if err != nil {
				logger.V(1).Info("Error checking pod health", "pod", oldPod.Name, "error", err)
				allHealthy = false
				continue
			}
			if !healthy {
				allHealthy = false
			}
		}

		if allHealthy {
			logger.Info("All replacement pods are healthy")
			return nil
		}

		time.Sleep(pollInterval)
	}

	return fmt.Errorf("timeout waiting for pods to become healthy")
}

// checkOwnerPodsHealthy checks if pods owned by the same controller are healthy
func (r *ConfigMapReconciler) checkOwnerPodsHealthy(ctx context.Context, oldPod *corev1.Pod) (bool, error) {
	// Get the controller owner reference
	var ownerRef *metav1.OwnerReference
	for i := range oldPod.OwnerReferences {
		if oldPod.OwnerReferences[i].Controller != nil && *oldPod.OwnerReferences[i].Controller {
			ownerRef = &oldPod.OwnerReferences[i]
			break
		}
	}

	if ownerRef == nil {
		// No controller - pod won't be recreated, consider it "healthy" (done)
		return true, nil
	}

	// List pods in the same namespace
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(oldPod.Namespace)); err != nil {
		return false, err
	}

	// Find pods with the same owner
	for _, pod := range pods.Items {
		for _, ref := range pod.OwnerReferences {
			if ref.UID == ownerRef.UID {
				// Check if this pod is ready
				if isPodReady(&pod) {
					return true, nil
				}
			}
		}
	}

	return false, nil
}

// isPodReady checks if a pod is in Ready condition
func isPodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// loadPDBs loads PodDisruptionBudgets for a namespace
func (r *ConfigMapReconciler) loadPDBs(ctx context.Context, namespace string) ([]policyv1.PodDisruptionBudget, error) {
	var pdbList policyv1.PodDisruptionBudgetList
	if err := r.List(ctx, &pdbList, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	return pdbList.Items, nil
}

// getIntOrPercentValue converts IntOrString to an int value
func getIntOrPercentValue(val *intstr.IntOrString, total int) int {
	if val.Type == intstr.Int {
		return val.IntValue()
	}
	// Percentage
	percent, _ := intstr.GetScaledValueFromIntOrPercent(val, total, true)
	return percent
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
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}).
		Complete(r)
}
