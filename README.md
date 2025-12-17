# k8s-autoapply-operator

Automatically restart pods when their ConfigMaps change.

## What it does

Kubernetes doesn't restart pods when a mounted ConfigMap changes. This operator watches for ConfigMap updates and automatically deletes (restarts) pods that reference them.

It detects ConfigMap usage via:
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

1. Operator starts and begins tracking ConfigMap versions
2. When a ConfigMap's ResourceVersion changes, it finds all pods in that namespace
3. For each pod that mounts/references the ConfigMap, it deletes the pod
4. The pod's controller (Deployment, StatefulSet, etc.) recreates it with the new ConfigMap

Note: The operator waits 10 seconds on startup before acting to avoid mass restarts.
