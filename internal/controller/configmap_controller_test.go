package controller

import (
	"context"
	"regexp"
	"testing"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	autoapplyv1alpha1 "github.com/manos/k8s-autoapply-operator/api/v1alpha1"
)

// ============================================================================
// Unit Tests
// ============================================================================

func TestPodUsesConfigMap(t *testing.T) {
	r := &ConfigMapReconciler{}

	tests := []struct {
		name          string
		pod           *corev1.Pod
		configMapName string
		expected      bool
	}{
		{
			name: "pod with configmap volume",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "my-config",
									},
								},
							},
						},
					},
				},
			},
			configMapName: "my-config",
			expected:      true,
		},
		{
			name: "pod with different configmap volume",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "other-config",
									},
								},
							},
						},
					},
				},
			},
			configMapName: "my-config",
			expected:      false,
		},
		{
			name: "pod with envFrom configmap",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "app",
							EnvFrom: []corev1.EnvFromSource{
								{
									ConfigMapRef: &corev1.ConfigMapEnvSource{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "my-config",
										},
									},
								},
							},
						},
					},
				},
			},
			configMapName: "my-config",
			expected:      true,
		},
		{
			name: "pod with env var from configmap",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "app",
							Env: []corev1.EnvVar{
								{
									Name: "MY_VAR",
									ValueFrom: &corev1.EnvVarSource{
										ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: "my-config",
											},
											Key: "some-key",
										},
									},
								},
							},
						},
					},
				},
			},
			configMapName: "my-config",
			expected:      true,
		},
		{
			name: "pod with projected volume containing configmap",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: "projected",
							VolumeSource: corev1.VolumeSource{
								Projected: &corev1.ProjectedVolumeSource{
									Sources: []corev1.VolumeProjection{
										{
											ConfigMap: &corev1.ConfigMapProjection{
												LocalObjectReference: corev1.LocalObjectReference{
													Name: "my-config",
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			configMapName: "my-config",
			expected:      true,
		},
		{
			name: "pod with init container using configmap",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{
						{
							Name: "init",
							EnvFrom: []corev1.EnvFromSource{
								{
									ConfigMapRef: &corev1.ConfigMapEnvSource{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "my-config",
										},
									},
								},
							},
						},
					},
				},
			},
			configMapName: "my-config",
			expected:      true,
		},
		{
			name: "pod without configmap",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx",
						},
					},
				},
			},
			configMapName: "my-config",
			expected:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := r.podUsesConfigMap(tt.pod, tt.configMapName)
			if result != tt.expected {
				t.Errorf("podUsesConfigMap() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestIsPodExcluded(t *testing.T) {
	r := &ConfigMapReconciler{}

	patterns := []*regexp.Regexp{
		regexp.MustCompile(`^kube-.*`),
		regexp.MustCompile(`.*-migration-.*`),
		regexp.MustCompile(`^test-pod$`),
	}

	tests := []struct {
		name     string
		podName  string
		expected bool
	}{
		{"kube-system pod", "kube-proxy-abc123", true},
		{"kube-dns pod", "kube-dns-xyz789", true},
		{"migration job", "app-migration-123", true},
		{"exact match", "test-pod", true},
		{"normal pod", "nginx-deployment-abc123", false},
		{"partial match not excluded", "my-kube-app", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := r.isPodExcluded(tt.podName, patterns)
			if result != tt.expected {
				t.Errorf("isPodExcluded(%s) = %v, expected %v", tt.podName, result, tt.expected)
			}
		})
	}
}

func TestPodsByOwner(t *testing.T) {
	ownerUID1 := types.UID("deployment-1")
	ownerUID2 := types.UID("statefulset-1")
	trueVal := true

	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "deploy-pod-1",
				OwnerReferences: []metav1.OwnerReference{
					{UID: ownerUID1, Controller: &trueVal},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "deploy-pod-2",
				OwnerReferences: []metav1.OwnerReference{
					{UID: ownerUID1, Controller: &trueVal},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "sts-pod-1",
				OwnerReferences: []metav1.OwnerReference{
					{UID: ownerUID2, Controller: &trueVal},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "standalone-pod",
			},
		},
	}

	groups := podsByOwner(pods)

	if len(groups) != 3 {
		t.Errorf("Expected 3 groups, got %d", len(groups))
	}

	if len(groups[ownerUID1]) != 2 {
		t.Errorf("Expected 2 pods for ownerUID1, got %d", len(groups[ownerUID1]))
	}

	if len(groups[ownerUID2]) != 1 {
		t.Errorf("Expected 1 pod for ownerUID2, got %d", len(groups[ownerUID2]))
	}

	// Standalone pods grouped under empty UID
	if len(groups[types.UID("")]) != 1 {
		t.Errorf("Expected 1 standalone pod, got %d", len(groups[types.UID("")]))
	}
}

func TestIsPodReady(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name: "ready pod",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					},
				},
			},
			expected: true,
		},
		{
			name: "not ready pod",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					Conditions: []corev1.PodCondition{
						{Type: corev1.PodReady, Status: corev1.ConditionFalse},
					},
				},
			},
			expected: false,
		},
		{
			name: "pending pod",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
				},
			},
			expected: false,
		},
		{
			name: "succeeded pod",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodSucceeded,
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isPodReady(tt.pod)
			if result != tt.expected {
				t.Errorf("isPodReady() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestGetIntOrPercentValue(t *testing.T) {
	tests := []struct {
		name     string
		val      *intstr.IntOrString
		total    int
		expected int
	}{
		{
			name:     "integer value",
			val:      intstrPtr(intstr.FromInt(3)),
			total:    10,
			expected: 3,
		},
		{
			name:     "percentage value 50%",
			val:      intstrPtr(intstr.FromString("50%")),
			total:    10,
			expected: 5,
		},
		{
			name:     "percentage value 30%",
			val:      intstrPtr(intstr.FromString("30%")),
			total:    10,
			expected: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getIntOrPercentValue(tt.val, tt.total)
			if result != tt.expected {
				t.Errorf("getIntOrPercentValue() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func intstrPtr(val intstr.IntOrString) *intstr.IntOrString {
	return &val
}

// ============================================================================
// Integration Tests with fake client
// ============================================================================

func setupTestReconciler() (*ConfigMapReconciler, client.Client) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = autoapplyv1alpha1.AddToScheme(scheme)
	_ = policyv1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	reconciler := &ConfigMapReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	return reconciler, fakeClient
}

func TestReconcile_FirstTimeConfigMap(t *testing.T) {
	r, fakeClient := setupTestReconciler()
	ctx := context.Background()

	// Create a ConfigMap
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config",
			Namespace: "default",
		},
		Data: map[string]string{"key": "value"},
	}
	if err := fakeClient.Create(ctx, cm); err != nil {
		t.Fatalf("Failed to create ConfigMap: %v", err)
	}

	// First reconcile should just track the ConfigMap
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-config",
			Namespace: "default",
		},
	}

	result, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	if result.Requeue {
		t.Error("Expected no requeue on first reconcile")
	}

	// Verify ConfigMap is being tracked
	key := req.String()
	if _, ok := r.configMapVersions.Load(key); !ok {
		t.Error("ConfigMap should be tracked after first reconcile")
	}
}

func TestReconcile_ConfigMapChange_RestartsPods(t *testing.T) {
	r, fakeClient := setupTestReconciler()
	ctx := context.Background()

	// Pre-track a "previous" version
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-config",
			Namespace: "default",
		},
	}
	r.configMapVersions.Store(req.String(), "old-version")

	// Create a ConfigMap with new version
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config",
			Namespace: "default",
		},
		Data: map[string]string{"key": "value"},
	}
	_ = fakeClient.Create(ctx, cm)

	// Create a pod that uses the ConfigMap
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "nginx",
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "config",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-config",
							},
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}
	_ = fakeClient.Create(ctx, pod)

	// Reconcile - should detect change and trigger restart
	_, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Verify pod was deleted (restarted)
	var pods corev1.PodList
	_ = fakeClient.List(ctx, &pods, client.InNamespace("default"))
	if len(pods.Items) != 0 {
		t.Errorf("Expected pod to be deleted, but found %d pods", len(pods.Items))
	}
}

func TestReconcile_ExcludedNamespace(t *testing.T) {
	r, fakeClient := setupTestReconciler()
	ctx := context.Background()

	// Create exclusion config
	cfg := &autoapplyv1alpha1.AutoApplyConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
		Spec: autoapplyv1alpha1.AutoApplyConfigSpec{
			ExcludeNamespaces: []string{"kube-system"},
		},
	}
	_ = fakeClient.Create(ctx, cfg)

	// Create a ConfigMap in excluded namespace
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-config",
			Namespace:       "kube-system",
			ResourceVersion: "1",
		},
	}
	_ = fakeClient.Create(ctx, cm)

	// Create a pod in excluded namespace
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod",
			Namespace: "kube-system",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "nginx"}},
			Volumes: []corev1.Volume{
				{
					Name: "config",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: "test-config"},
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	_ = fakeClient.Create(ctx, pod)

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-config",
			Namespace: "kube-system",
		},
	}

	// First reconcile
	_, _ = r.Reconcile(ctx, req)

	// Simulate change
	r.configMapVersions.Store(req.String(), "0")

	// Second reconcile - should skip due to exclusion
	_, _ = r.Reconcile(ctx, req)

	// Verify pod was NOT deleted
	var pods corev1.PodList
	_ = fakeClient.List(ctx, &pods, client.InNamespace("kube-system"))
	if len(pods.Items) != 1 {
		t.Errorf("Expected pod to NOT be deleted in excluded namespace, found %d pods", len(pods.Items))
	}
}

func TestReconcile_YoloMode(t *testing.T) {
	r, fakeClient := setupTestReconciler()
	ctx := context.Background()

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-config", Namespace: "default"},
	}

	// Pre-track old version
	r.configMapVersions.Store(req.String(), "old-version")

	// Create yolo config
	cfg := &autoapplyv1alpha1.AutoApplyConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "yolo",
		},
		Spec: autoapplyv1alpha1.AutoApplyConfigSpec{
			YoloMode: true,
		},
	}
	_ = fakeClient.Create(ctx, cfg)

	// Create ConfigMap
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config",
			Namespace: "default",
		},
	}
	_ = fakeClient.Create(ctx, cm)

	// Create multiple pods
	for i := 0; i < 5; i++ {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod-" + string(rune('a'+i)),
				Namespace: "default",
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "app", Image: "nginx"}},
				Volumes: []corev1.Volume{
					{
						Name: "config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: "test-config"},
							},
						},
					},
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}
		_ = fakeClient.Create(ctx, pod)
	}

	// Reconcile - YOLO mode should delete all pods at once
	_, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Verify all pods were deleted
	var pods corev1.PodList
	_ = fakeClient.List(ctx, &pods, client.InNamespace("default"))
	if len(pods.Items) != 0 {
		t.Errorf("YOLO mode should delete all pods, found %d remaining", len(pods.Items))
	}
}

func TestReconcile_ExcludedPodPattern(t *testing.T) {
	r, fakeClient := setupTestReconciler()
	ctx := context.Background()

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-config", Namespace: "default"},
	}

	// Pre-track old version
	r.configMapVersions.Store(req.String(), "old-version")

	// Create exclusion config
	cfg := &autoapplyv1alpha1.AutoApplyConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "default",
		},
		Spec: autoapplyv1alpha1.AutoApplyConfigSpec{
			ExcludePods: []string{"^excluded-.*"},
		},
	}
	_ = fakeClient.Create(ctx, cfg)

	// Create ConfigMap
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config",
			Namespace: "default",
		},
	}
	_ = fakeClient.Create(ctx, cm)

	// Create pods - one excluded, one not
	excludedPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "excluded-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "nginx"}},
			Volumes: []corev1.Volume{{
				Name: "config",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-config"},
					},
				},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	normalPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "normal-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "nginx"}},
			Volumes: []corev1.Volume{{
				Name: "config",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-config"},
					},
				},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	_ = fakeClient.Create(ctx, excludedPod)
	_ = fakeClient.Create(ctx, normalPod)

	// Reconcile
	_, _ = r.Reconcile(ctx, req)

	// Verify only excluded pod remains
	var pods corev1.PodList
	_ = fakeClient.List(ctx, &pods, client.InNamespace("default"))
	if len(pods.Items) != 1 {
		t.Errorf("Expected 1 pod (excluded), found %d", len(pods.Items))
	}
	if len(pods.Items) > 0 && pods.Items[0].Name != "excluded-pod" {
		t.Errorf("Expected excluded-pod to remain, but found %s", pods.Items[0].Name)
	}
}

func TestCanDeletePod_NoPDB(t *testing.T) {
	r, _ := setupTestReconciler()
	ctx := context.Background()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "test-pod",
			Labels: map[string]string{"app": "test"},
		},
	}

	// No PDBs - should always allow deletion
	canDelete := r.canDeletePod(ctx, pod, nil)
	if !canDelete {
		t.Error("Should allow deletion when no PDBs exist")
	}
}

func TestCanDeletePod_WithPDB(t *testing.T) {
	r, _ := setupTestReconciler()
	ctx := context.Background()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "test-pod",
			Labels: map[string]string{"app": "test"},
		},
	}

	tests := []struct {
		name               string
		disruptionsAllowed int32
		expected           bool
	}{
		{"disruptions allowed", 1, true},
		{"no disruptions allowed", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pdb := policyv1.PodDisruptionBudget{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pdb"},
				Spec: policyv1.PodDisruptionBudgetSpec{
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "test"},
					},
				},
				Status: policyv1.PodDisruptionBudgetStatus{
					DisruptionsAllowed: tt.disruptionsAllowed,
				},
			}

			canDelete := r.canDeletePod(ctx, pod, []policyv1.PodDisruptionBudget{pdb})
			if canDelete != tt.expected {
				t.Errorf("canDeletePod() = %v, expected %v", canDelete, tt.expected)
			}
		})
	}
}

func TestFindPodsUsingConfigMap(t *testing.T) {
	r, fakeClient := setupTestReconciler()
	ctx := context.Background()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config",
			Namespace: "default",
		},
	}

	// Pod using the ConfigMap
	usingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "using-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "nginx"}},
			Volumes: []corev1.Volume{{
				Name: "config",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-config"},
					},
				},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	// Pod not using the ConfigMap
	notUsingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "not-using-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "nginx"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	// Completed pod (should be skipped)
	completedPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "completed-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "app", Image: "nginx"}},
			Volumes: []corev1.Volume{{
				Name: "config",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-config"},
					},
				},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
	}

	_ = fakeClient.Create(ctx, usingPod)
	_ = fakeClient.Create(ctx, notUsingPod)
	_ = fakeClient.Create(ctx, completedPod)

	pods := r.findPodsUsingConfigMap(ctx, cm, nil)

	if len(pods) != 1 {
		t.Errorf("Expected 1 pod, found %d", len(pods))
	}
	if len(pods) > 0 && pods[0].Name != "using-pod" {
		t.Errorf("Expected using-pod, found %s", pods[0].Name)
	}
}

func TestLoadConfig(t *testing.T) {
	r, fakeClient := setupTestReconciler()
	ctx := context.Background()

	// Create multiple configs
	cfg1 := &autoapplyv1alpha1.AutoApplyConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "config1"},
		Spec: autoapplyv1alpha1.AutoApplyConfigSpec{
			ExcludePods:       []string{"^kube-.*"},
			ExcludeNamespaces: []string{"kube-system"},
		},
	}
	cfg2 := &autoapplyv1alpha1.AutoApplyConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "config2"},
		Spec: autoapplyv1alpha1.AutoApplyConfigSpec{
			ExcludePods:       []string{".*-job$"},
			ExcludeNamespaces: []string{"cert-manager"},
			YoloMode:          true,
		},
	}

	_ = fakeClient.Create(ctx, cfg1)
	_ = fakeClient.Create(ctx, cfg2)

	config := r.loadConfig(ctx)

	// Should merge all configs
	if len(config.excludePodPatterns) != 2 {
		t.Errorf("Expected 2 exclude patterns, got %d", len(config.excludePodPatterns))
	}
	if len(config.excludeNamespaces) != 2 {
		t.Errorf("Expected 2 exclude namespaces, got %d", len(config.excludeNamespaces))
	}
	if !config.yoloMode {
		t.Error("Expected yoloMode to be true (any config enabling it)")
	}
}

// ============================================================================
// Benchmark Tests
// ============================================================================

func BenchmarkPodUsesConfigMap(b *testing.B) {
	r := &ConfigMapReconciler{}
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "app",
					EnvFrom: []corev1.EnvFromSource{
						{ConfigMapRef: &corev1.ConfigMapEnvSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: "config-1"},
						}},
					},
					Env: []corev1.EnvVar{
						{Name: "VAR1", ValueFrom: &corev1.EnvVarSource{
							ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "config-2"},
							},
						}},
					},
				},
			},
			Volumes: []corev1.Volume{
				{Name: "vol1", VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "config-3"},
					},
				}},
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.podUsesConfigMap(pod, "config-2")
	}
}

func BenchmarkIsPodExcluded(b *testing.B) {
	r := &ConfigMapReconciler{}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`^kube-.*`),
		regexp.MustCompile(`.*-migration-.*`),
		regexp.MustCompile(`^test-.*`),
		regexp.MustCompile(`.*-job$`),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.isPodExcluded("nginx-deployment-abc123", patterns)
	}
}

