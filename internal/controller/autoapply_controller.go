package controller

import (
	"bytes"
	"context"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	autoapplyv1alpha1 "github.com/charlie/k8s-autoapply-operator/api/v1alpha1"
)

// AutoApplyReconciler reconciles an AutoApply object
type AutoApplyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=autoapply.io,resources=autoapplies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=autoapply.io,resources=autoapplies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=autoapply.io,resources=autoapplies/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="*",resources="*",verbs=get;list;watch;create;update;patch;delete

// Reconcile handles the reconciliation loop for AutoApply resources
func (r *AutoApplyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the AutoApply instance
	var autoApply autoapplyv1alpha1.AutoApply
	if err := r.Get(ctx, req.NamespacedName, &autoApply); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger.Info("Reconciling AutoApply", "name", autoApply.Name, "namespace", autoApply.Namespace)

	// Determine ConfigMap namespace
	cmNamespace := autoApply.Spec.ConfigMapRef.Namespace
	if cmNamespace == "" {
		cmNamespace = autoApply.Namespace
	}

	// Fetch the referenced ConfigMap
	var configMap corev1.ConfigMap
	cmKey := types.NamespacedName{
		Name:      autoApply.Spec.ConfigMapRef.Name,
		Namespace: cmNamespace,
	}
	if err := r.Get(ctx, cmKey, &configMap); err != nil {
		logger.Error(err, "Failed to fetch ConfigMap", "configmap", cmKey)
		r.setCondition(&autoApply, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "ConfigMapNotFound",
			Message:            fmt.Sprintf("ConfigMap %s/%s not found: %v", cmNamespace, autoApply.Spec.ConfigMapRef.Name, err),
			ObservedGeneration: autoApply.Generation,
		})
		if err := r.Status().Update(ctx, &autoApply); err != nil {
			logger.Error(err, "Failed to update status")
		}
		return ctrl.Result{}, err
	}

	// Check if ConfigMap has changed
	if configMap.ResourceVersion == autoApply.Status.LastAppliedConfigMapResourceVersion {
		logger.Info("ConfigMap unchanged, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	// Parse and apply manifests from ConfigMap
	var appliedResources []autoapplyv1alpha1.ResourceReference
	var applyErrors []error

	for key, data := range configMap.Data {
		logger.Info("Processing ConfigMap key", "key", key)

		resources, err := r.parseManifests([]byte(data))
		if err != nil {
			logger.Error(err, "Failed to parse manifests", "key", key)
			applyErrors = append(applyErrors, fmt.Errorf("key %s: %w", key, err))
			continue
		}

		for _, resource := range resources {
			if err := r.applyResource(ctx, &autoApply, resource); err != nil {
				logger.Error(err, "Failed to apply resource",
					"kind", resource.GetKind(),
					"name", resource.GetName(),
					"namespace", resource.GetNamespace())
				applyErrors = append(applyErrors, err)
				continue
			}

			appliedResources = append(appliedResources, autoapplyv1alpha1.ResourceReference{
				APIVersion: resource.GetAPIVersion(),
				Kind:       resource.GetKind(),
				Name:       resource.GetName(),
				Namespace:  resource.GetNamespace(),
			})

			logger.Info("Applied resource",
				"kind", resource.GetKind(),
				"name", resource.GetName(),
				"namespace", resource.GetNamespace())
		}
	}

	// Handle pruning if enabled
	if autoApply.Spec.Prune {
		if err := r.pruneResources(ctx, &autoApply, appliedResources); err != nil {
			logger.Error(err, "Failed to prune resources")
			applyErrors = append(applyErrors, err)
		}
	}

	// Update status
	autoApply.Status.AppliedResources = appliedResources
	autoApply.Status.LastAppliedConfigMapResourceVersion = configMap.ResourceVersion
	autoApply.Status.ObservedGeneration = autoApply.Generation

	if len(applyErrors) > 0 {
		r.setCondition(&autoApply, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "ApplyFailed",
			Message:            fmt.Sprintf("Failed to apply some resources: %v", applyErrors),
			ObservedGeneration: autoApply.Generation,
		})
	} else {
		r.setCondition(&autoApply, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "Applied",
			Message:            fmt.Sprintf("Successfully applied %d resources", len(appliedResources)),
			ObservedGeneration: autoApply.Generation,
		})
	}

	if err := r.Status().Update(ctx, &autoApply); err != nil {
		logger.Error(err, "Failed to update status")
		return ctrl.Result{}, err
	}

	logger.Info("Reconciliation complete", "appliedCount", len(appliedResources))
	return ctrl.Result{}, nil
}

// parseManifests parses YAML manifests into unstructured objects
func (r *AutoApplyReconciler) parseManifests(data []byte) ([]*unstructured.Unstructured, error) {
	var resources []*unstructured.Unstructured

	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		var obj unstructured.Unstructured
		if err := decoder.Decode(&obj); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		// Skip empty documents
		if obj.GetAPIVersion() == "" || obj.GetKind() == "" {
			continue
		}

		resources = append(resources, &obj)
	}

	return resources, nil
}

// applyResource applies a single resource to the cluster
func (r *AutoApplyReconciler) applyResource(ctx context.Context, autoApply *autoapplyv1alpha1.AutoApply, resource *unstructured.Unstructured) error {
	// Set owner reference for namespaced resources in the same namespace
	if resource.GetNamespace() == "" || resource.GetNamespace() == autoApply.Namespace {
		if resource.GetNamespace() == "" {
			// For namespaced resources without namespace, use AutoApply's namespace
			gvk := resource.GroupVersionKind()
			mapping, err := r.RESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
			if err == nil && mapping.Scope.Name() == meta.RESTScopeNameNamespace {
				resource.SetNamespace(autoApply.Namespace)
			}
		}

		if resource.GetNamespace() == autoApply.Namespace {
			resource.SetOwnerReferences([]metav1.OwnerReference{
				{
					APIVersion: autoApply.APIVersion,
					Kind:       autoApply.Kind,
					Name:       autoApply.Name,
					UID:        autoApply.UID,
					Controller: ptr(true),
				},
			})
		}
	}

	// Server-side apply
	return r.Patch(ctx, resource, client.Apply, client.FieldOwner("autoapply-controller"), client.ForceOwnership)
}

// pruneResources removes resources that are no longer in the ConfigMap
func (r *AutoApplyReconciler) pruneResources(ctx context.Context, autoApply *autoapplyv1alpha1.AutoApply, currentResources []autoapplyv1alpha1.ResourceReference) error {
	logger := log.FromContext(ctx)

	// Build a set of current resources
	currentSet := make(map[string]struct{})
	for _, res := range currentResources {
		key := fmt.Sprintf("%s/%s/%s/%s", res.APIVersion, res.Kind, res.Namespace, res.Name)
		currentSet[key] = struct{}{}
	}

	// Find resources to prune
	for _, res := range autoApply.Status.AppliedResources {
		key := fmt.Sprintf("%s/%s/%s/%s", res.APIVersion, res.Kind, res.Namespace, res.Name)
		if _, exists := currentSet[key]; !exists {
			// Resource no longer in ConfigMap, delete it
			obj := &unstructured.Unstructured{}
			obj.SetAPIVersion(res.APIVersion)
			obj.SetKind(res.Kind)
			obj.SetName(res.Name)
			obj.SetNamespace(res.Namespace)

			if err := r.Delete(ctx, obj); err != nil {
				if client.IgnoreNotFound(err) != nil {
					return err
				}
			}
			logger.Info("Pruned resource", "kind", res.Kind, "name", res.Name, "namespace", res.Namespace)
		}
	}

	return nil
}

// setCondition updates or adds a condition
func (r *AutoApplyReconciler) setCondition(autoApply *autoapplyv1alpha1.AutoApply, condition metav1.Condition) {
	condition.LastTransitionTime = metav1.Now()
	meta.SetStatusCondition(&autoApply.Status.Conditions, condition)
}

// SetupWithManager sets up the controller with the Manager
func (r *AutoApplyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&autoapplyv1alpha1.AutoApply{}).
		Watches(
			&corev1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(r.findAutoAppliesForConfigMap),
		).
		Complete(r)
}

// findAutoAppliesForConfigMap returns reconcile requests for AutoApply resources
// that reference the given ConfigMap
func (r *AutoApplyReconciler) findAutoAppliesForConfigMap(ctx context.Context, obj client.Object) []reconcile.Request {
	configMap := obj.(*corev1.ConfigMap)
	logger := log.FromContext(ctx)

	var autoApplyList autoapplyv1alpha1.AutoApplyList
	if err := r.List(ctx, &autoApplyList); err != nil {
		logger.Error(err, "Failed to list AutoApply resources")
		return nil
	}

	var requests []reconcile.Request
	for _, aa := range autoApplyList.Items {
		cmNamespace := aa.Spec.ConfigMapRef.Namespace
		if cmNamespace == "" {
			cmNamespace = aa.Namespace
		}

		if aa.Spec.ConfigMapRef.Name == configMap.Name && cmNamespace == configMap.Namespace {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      aa.Name,
					Namespace: aa.Namespace,
				},
			})
		}
	}

	return requests
}

func ptr[T any](v T) *T {
	return &v
}
