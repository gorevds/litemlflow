# LiteMLflow Operator

A Kubernetes operator that manages `LiteMLflow` custom resources. It reconciles
each CR into a StatefulSet, two Services (headless + ClusterIP), and a
PersistentVolumeClaim.

The operator lives in a **separate Go module** (`github.com/litemlflow/litemlflow-operator`)
so that `controller-runtime` and its transitive dependencies are never added to
the main LiteMLflow server module.

## Module layout

```
operator/
├── go.mod
├── main.go                             # controller-runtime manager bootstrap
├── api/v1alpha1/
│   ├── litemlflow_types.go             # CRD Go types
│   ├── groupversion_info.go
│   └── zz_generated.deepcopy.go
├── controllers/
│   ├── litemlflow_controller.go        # reconciliation logic
│   └── litemlflow_controller_test.go
├── config/
│   ├── crd/litemlflow.yaml             # CRD manifest (apply first)
│   ├── rbac/service_account.yaml
│   ├── rbac/role.yaml
│   ├── rbac/role_binding.yaml
│   └── manager/manager.yaml            # operator Deployment
└── README.md
```

## Quick start

### 1. Build

From the repo root:

```bash
make operator-build
# → bin/litemlflow-operator
```

Or directly from the module directory:

```bash
cd operator && go build -o ../bin/litemlflow-operator ./
```

### 2. Containerise

```bash
docker build -t ghcr.io/your-org/litemlflow-operator:v0.1.0 \
  -f - . <<'EOF'
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY operator/ operator/
WORKDIR /src/operator
RUN go build -o /litemlflow-operator ./

FROM gcr.io/distroless/static:nonroot
COPY --from=build /litemlflow-operator /
ENTRYPOINT ["/litemlflow-operator"]
EOF

docker push ghcr.io/your-org/litemlflow-operator:v0.1.0
```

Update `config/manager/manager.yaml` → `image:` with the pushed tag.

### 3. Install the CRD

```bash
kubectl apply -f operator/config/crd/litemlflow.yaml
```

### 4. Apply RBAC

```bash
kubectl apply -f operator/config/rbac/service_account.yaml
kubectl apply -f operator/config/rbac/role.yaml
kubectl apply -f operator/config/rbac/role_binding.yaml
```

### 5. Deploy the operator

```bash
kubectl apply -f operator/config/manager/manager.yaml
kubectl -n litemlflow-system wait deploy/litemlflow-operator --for=condition=available
```

### 6. Create a LiteMLflow instance

```yaml
# lmf-prod.yaml
apiVersion: litemlflow.dev/v1alpha1
kind: LiteMLflow
metadata:
  name: lmf-prod
  namespace: ml
spec:
  version: "v1.0.0-rc1"
  replicas: 1          # must be 1 (SQLite single-writer)
  storage:
    size: "20Gi"
  auth:
    mode: "none"       # none | basic | oidc
  artifactBackend: "fs"
  resources:
    requests:
      cpu: "100m"
      memory: "256Mi"
```

```bash
kubectl create namespace ml
kubectl apply -f lmf-prod.yaml
kubectl -n ml get litemlflows
# NAME       VERSION         READY   AGE
# lmf-prod   v1.0.0-rc1      true    30s
```

## Basic-auth mode

Create a Secret with the username and bcrypt-hashed password:

```bash
kubectl -n ml create secret generic lmf-creds \
  --from-literal=user=admin \
  --from-literal=pass-hash='$2a$12$...'   # bcrypt hash
```

Then reference it in the CR:

```yaml
spec:
  auth:
    mode: basic
    basicUserSecret:
      name: lmf-creds
      key: user
    basicPassHashSecret:
      name: lmf-creds
      key: pass-hash
```

If the Secret does not exist, the operator sets a `MissingSecret` condition on
the CR status — the StatefulSet is still created but the container will fail to
start until the Secret is present.

## S3 artifact backend

```bash
kubectl -n ml create secret generic lmf-s3 \
  --from-literal=access=AKIAIOSFODNN7EXAMPLE \
  --from-literal=secret=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
```

```yaml
spec:
  artifactBackend: s3
  s3:
    endpoint: https://s3.amazonaws.com
    bucket: my-ml-artifacts
    region: eu-west-1
    accessKeySecret:
      name: lmf-s3
      key: access
    secretKeySecret:
      name: lmf-s3
      key: secret
```

## Tests

```bash
# Pure unit tests (no cluster required)
make operator-test
# or:
cd operator && go test ./...

# Integration tests against a real API server (requires kubebuilder tools)
KUBEBUILDER_ASSETS=/path/to/kubebuilder/bin cd operator && go test ./...
```

## Separate repo recommendation

The operator release cadence (tied to Kubernetes API and controller-runtime
versions) is independent from the LiteMLflow server release cadence (tied to
Go, SQLite, and the MLflow client API surface). We recommend eventually
extracting this directory into a dedicated `litemlflow-operator` repository
so that operator hotfixes do not require a server release tag and vice-versa.
The module boundary (`go.mod`) is already drawn; moving to a separate repo is
a matter of updating the import paths and adding a CI pipeline.
