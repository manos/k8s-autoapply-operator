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
- Restarts 50% of affected pods first
- Waits for replacement pods to be healthy
- Only then restarts the remaining 50%
- Respects PodDisruptionBudgets — waits for PDB to allow deletion

**Detects ConfigMap usage via:**
- Volume mounts (`volumes[].configMap`)
- Projected volumes
- `envFrom` ConfigMap references
- Individual `env` vars from ConfigMaps

## Configuration (Optional)

By default, the operator restarts ALL pods that use a changed ConfigMap. To exclude certain pods or namespaces:

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
    - kube-system
    - cert-manager
```

## How it works

1. Operator watches all ConfigMaps for changes
2. When a ConfigMap changes, finds pods that reference it
3. Splits affected pods into two batches (50/50)
4. For each pod in first batch:
   - Waits for PDB to allow deletion (up to 5 min)
   - Deletes pod
5. Waits for replacement pods to be healthy (Running + Ready)
6. Repeats for second batch

This ensures you never take down more than 50% of pods at once, and respects PodDisruptionBudgets.

## Development

```bash
# Run locally (uses current kubeconfig)
make run

# Build container image
make docker-build IMG=ghcr.io/manos/k8s-autoapply-operator:tag

# Push image
make docker-push IMG=ghcr.io/manos/k8s-autoapply-operator:tag
```

## License

Apache 2.0
