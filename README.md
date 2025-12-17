# k8s-autoapply-operator

Automatically restart pods when their ConfigMaps change.

## Quick Install

```bash
kubectl apply -f https://raw.githubusercontent.com/manos/k8s-autoapply-operator/main/install.yaml
```

That's it! The operator now watches all ConfigMaps and restarts pods that use them.

## Uninstall

```bash
kubectl delete -f https://raw.githubusercontent.com/manos/k8s-autoapply-operator/main/install.yaml
```

## What it does

Kubernetes doesn't restart pods when a mounted ConfigMap changes. This operator watches for ConfigMap updates and automatically restarts affected pods.

**Safe rolling restarts:**
- Groups pods by owner (Deployment, StatefulSet, ReplicaSet)
- Restarts 50% of each owner's pods first (or less, whatever PodDisruptionBudget allows)
- Waits for replacement pods to be healthy
- Only then restarts the remaining 50% per owner (respecting PDB)
- Respects PodDisruptionBudgets — waits for PDB to allow deletion

**Detects ConfigMap usage via:**
- Volume mounts (`volumes[].configMap`)
- Projected volumes
- `envFrom` ConfigMap references
- Individual `env` vars from ConfigMaps

## Default Exclusions

The following are **always excluded** (built-in safe defaults):

| Exclusion | Reason |
|-----------|--------|
| `kube-system` namespace | Critical system components |
| `^coredns-.*` pods | Cluster DNS resolution |
| `.*-csi-.*` pods | Storage drivers |

## Configuration (Optional)

Create an `AutoApplyConfig` to add additional exclusions:

```yaml
apiVersion: autoapply.io/v1alpha1
kind: AutoApplyConfig
metadata:
  name: default
spec:
  excludePods:
    - "^kube-.*"           # Regex: exclude pods starting with kube-
    - ".*-migration-.*"    # Regex: exclude migration jobs
  excludeNamespaces:
    - cert-manager
  yoloMode: false          # Set to true to restart all pods at once (no rolling restart)
```

### Recommended Full Exclusions

For production clusters, consider excluding critical infrastructure:

```yaml
apiVersion: autoapply.io/v1alpha1
kind: AutoApplyConfig
metadata:
  name: production-safe
spec:
  excludeNamespaces:
    # Already excluded by default: kube-system
    - kube-public
    - kube-node-lease
    - cert-manager
    - ingress-nginx       # or your ingress namespace
    - monitoring          # prometheus, grafana
    - flux-system         # if using Flux
    - argocd              # if using ArgoCD
  excludePods:
    # Already excluded by default: ^coredns-.*, .*-csi-.*
    
    # Control plane (if running in-cluster)
    - "^kube-apiserver-.*"
    - "^kube-controller-manager-.*"
    - "^kube-scheduler-.*"
    - "^etcd-.*"
    
    # CNI plugins
    - "^calico-.*"
    - "^cilium-.*"
    - "^flannel-.*"
    - "^weave-.*"
    
    # Jobs (one-time, shouldn't restart)
    - ".*-job-.*"
```

| Pattern | Reason |
|---------|--------|
| CNI pods | Restarting can break node networking |
| Control plane | Can destabilize cluster |
| Jobs | They're meant to run once |

### YOLO Mode

If you're feeling brave (or testing in dev), enable `yoloMode` to skip all safety measures:

```yaml
apiVersion: autoapply.io/v1alpha1
kind: AutoApplyConfig
metadata:
  name: yolo
spec:
  yoloMode: true  # 🔥 Restarts ALL affected pods simultaneously (ignores batching)
```

**Note:** YOLO mode still respects exclusions, it just skips the 50/50 rolling restart.

## How it works

1. Operator watches all ConfigMaps for changes
2. When a ConfigMap changes, finds pods that reference it
3. Groups pods by their owner (Deployment/StatefulSet/ReplicaSet)
4. Splits each owner's pods into two batches (50/50)
5. First batch: deletes 50% from each owner (waits for PDB to allow)
6. Waits for replacement pods to be healthy (Running + Ready)
7. Second batch: deletes remaining 50% from each owner

This ensures you never take down more than 50% of any single Deployment/StatefulSet at once.

## Development

```bash
# Run locally (uses current kubeconfig)
make run

# Run tests
make test

# Build container image
make docker-build IMG=ghcr.io/manos/k8s-autoapply-operator:tag

# Push image
make docker-push IMG=ghcr.io/manos/k8s-autoapply-operator:tag
```

## License

MIT
