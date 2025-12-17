# k8s-autoapply-operator

A Kubernetes operator that watches ConfigMaps and automatically applies their manifest contents to the cluster.

## Description

The AutoApply operator watches for `AutoApply` custom resources that reference ConfigMaps containing Kubernetes manifests. When the referenced ConfigMap changes, the operator automatically applies the updated manifests to the cluster.

**Key features:**
- Watches ConfigMaps for changes and auto-applies manifests
- Supports multi-document YAML in ConfigMap data
- Optional pruning of resources removed from the ConfigMap
- Tracks applied resources in status

## Getting Started

### Prerequisites

- Go 1.22+
- kubectl configured with cluster access
- A Kubernetes cluster (v1.28+)

### Installation

1. Install the CRD:

```bash
make install
```

2. Deploy the controller:

```bash
make deploy
```

### Usage

1. Create a ConfigMap with your manifests:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-app-manifests
  namespace: default
data:
  deployment.yaml: |
    apiVersion: apps/v1
    kind: Deployment
    metadata:
      name: nginx
    spec:
      replicas: 1
      selector:
        matchLabels:
          app: nginx
      template:
        metadata:
          labels:
            app: nginx
        spec:
          containers:
          - name: nginx
            image: nginx:latest
  service.yaml: |
    apiVersion: v1
    kind: Service
    metadata:
      name: nginx
    spec:
      selector:
        app: nginx
      ports:
      - port: 80
```

2. Create an AutoApply resource to watch it:

```yaml
apiVersion: autoapply.io/v1alpha1
kind: AutoApply
metadata:
  name: my-app
  namespace: default
spec:
  configMapRef:
    name: my-app-manifests
  prune: true  # Delete resources when removed from ConfigMap
```

Now when you update the ConfigMap, the operator will automatically apply the changes!

## Development

### Building

```bash
# Build the binary
make build

# Run locally
make run

# Build Docker image
make docker-build IMG=my-registry/k8s-autoapply-operator:tag
```

### Testing

```bash
make test
```

## Project Structure

```
├── api/v1alpha1/          # CRD type definitions
├── cmd/manager/           # Operator entrypoint
├── config/
│   ├── crd/              # CRD manifests
│   ├── manager/          # Deployment manifests
│   └── rbac/             # RBAC manifests
├── internal/controller/   # Reconciliation logic
├── Dockerfile
├── Makefile
└── go.mod
```

## License

Apache 2.0
