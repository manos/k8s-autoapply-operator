# k8s-autoapply-operator

Automatically restart pods when their ConfigMaps change.

## What it does

Kubernetes doesn't restart pods when a mounted ConfigMap changes. This operator watches for ConfigMap updates and automatically restarts pods that reference them.

**Safe rolling restarts:**
- Restarts 50% of affected pods first
- Waits 5 seconds, then checks if replacement pods are healthy
- Only restarts the remaining 50% if the first batch is healthy
- Respects PodDisruptionBudgets - won't delete pods that would violate a PDB

**Detects ConfigMap usage via:**
- Volume mounts (`volumes[].configMap`)
- Projected volumes
- `envFrom` ConfigMap references
- Individual `env` vars from ConfigMaps

## Installation

```bash
# Install CRD (optional, only needed for exclusions)
kubectl apply -f config/crd/

# Install RBAC
kubectl apply -f config/rbac/

# Deploy the operator
kubectl apply -f config/manager/
```

## Configuration (Optional)

By default, the operator restarts ALL pods that use a changed ConfigMap. To exclude certain pods, create an `AutoApplyConfig`:

```yaml
apiVersion: autoapply.io/v1alpha1
kind: AutoApplyConfig
metadata:
  name: default
spec:
  excludePods:
    - "^kube-.*"           # Exclude kube-system pods
    - ".*-migration-.*"    # Exclude migration jobs
  excludeNamespaces:
    - kube-system
    - cert-manager
```

## Development

```bash
# Build
make build

# Run locally
make run

# Build Docker image
make docker-build IMG=your-registry/k8s-autoapply-operator:tag
```

## How it works

1. Operator watches all ConfigMaps for changes
2. When a ConfigMap's ResourceVersion changes, finds all pods that reference it
3. Splits affected pods into two batches (50/50)
4. Deletes first batch (respecting PDBs)
5. Waits 5 seconds, then verifies replacement pods are healthy (Running + Ready)
6. If healthy, deletes second batch
7. If first batch isn't healthy within 60s, aborts and doesn't touch second batch

This ensures you never take down more than 50% of pods at once, and only proceed if the first batch recovers successfully.
