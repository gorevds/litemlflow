# LiteMLflow Helm Chart

Helm chart for [LiteMLflow](https://litemlflow.dev) — a single-binary,
MLflow-API-compatible experiment tracker with first-class LLM tracing.

## Install

```bash
# From OCI registry (once published):
helm install lmf oci://ghcr.io/litemlflow/charts/litemlflow --version 0.1.0

# From a local clone of this repo:
helm install lmf dist/helm/litemlflow/
```

## Quick start (no auth, ephemeral storage)

```bash
helm install lmf dist/helm/litemlflow/ \
  --set persistence.enabled=false \
  --set config.authMode=none
```

## With basic auth and persistent storage

```bash
HASH=$(printf 'hunter2' | sha256sum | awk '{print $1}')

helm install lmf dist/helm/litemlflow/ \
  --set config.authMode=basic \
  --set auth.user=alice \
  --set auth.passHash="$HASH" \
  --set persistence.size=20Gi
```

## With Ingress (nginx)

```bash
helm install lmf dist/helm/litemlflow/ \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.hosts[0].host=litemlflow.example.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix \
  --set ingress.tls[0].secretName=litemlflow-tls \
  --set ingress.tls[0].hosts[0]=litemlflow.example.com
```

## With Prometheus ServiceMonitor

```bash
helm install lmf dist/helm/litemlflow/ \
  --set metrics.serviceMonitor.enabled=true \
  --set metrics.serviceMonitor.additionalLabels.release=prometheus
```

## Configuration reference

| Key | Default | Description |
|-----|---------|-------------|
| `image.repository` | `litemlflow/litemlflow` | Container image repository |
| `image.tag` | `v0.4.0-rc1` | Image tag (defaults to `appVersion`) |
| `config.authMode` | `none` | Auth mode: `none` or `basic` |
| `auth.user` | `""` | Basic auth username |
| `auth.passHash` | `""` | SHA-256 hex of the password |
| `service.type` | `ClusterIP` | Kubernetes service type |
| `service.port` | `5000` | Service port |
| `ingress.enabled` | `false` | Enable Ingress resource |
| `persistence.enabled` | `true` | Use a PersistentVolumeClaim |
| `persistence.size` | `10Gi` | PVC size |
| `persistence.storageClass` | `""` | Storage class (cluster default if empty) |
| `resources.limits.cpu` | `500m` | CPU limit |
| `resources.limits.memory` | `256Mi` | Memory limit |
| `metrics.serviceMonitor.enabled` | `false` | Create Prometheus ServiceMonitor |

## Architecture note

The chart uses a **StatefulSet** (not a plain Deployment) because LiteMLflow
stores all state in a single SQLite database file on disk. The StatefulSet
volumeClaimTemplate gives each pod a stable, dedicated PVC so the data survives
pod rescheduling without a separate NFS/RWX storage class. Single-replica is
the only supported mode; horizontal scaling requires an S3-backed artifact
store (coming in v0.2).

## Upgrading

```bash
helm upgrade lmf dist/helm/litemlflow/ --reuse-values
```

## Uninstall

```bash
helm uninstall lmf
# PVCs are retained by default; delete manually when safe:
kubectl delete pvc data-lmf-litemlflow-0
```
