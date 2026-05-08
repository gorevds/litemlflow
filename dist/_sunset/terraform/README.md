# terraform-provider-litemlflow

Terraform provider for [LiteMLflow](https://github.com/gorevds/litemlflow).

Manages experiments, prompts, registered models, and workspaces as
infrastructure-as-code using the [HashiCorp Plugin Framework](https://developer.hashicorp.com/terraform/plugin/framework).

## Requirements

- Terraform 1.5+
- Go 1.22+ (to build from source)
- A running LiteMLflow server (v0.1+)

## Install

### From the Terraform Registry (once published)

```hcl
terraform {
  required_providers {
    litemlflow = {
      source  = "gorevds/litemlflow"
      version = "~> 0.1"
    }
  }
}
```

### From source (dev_overrides)

```bash
# Build the provider binary.
make terraform-build
# → bin/terraform-provider-litemlflow

# Tell Terraform to use the local binary.
# Add to ~/.terraformrc:
#   provider_installation {
#     dev_overrides { "gorevds/litemlflow" = "/path/to/repo/bin" }
#     direct {}
#   }
```

## Provider configuration

```hcl
provider "litemlflow" {
  url      = "https://lmf.example.com"  # or LITEMLFLOW_URL
  auth     = "basic"                     # or LITEMLFLOW_AUTH
  username = "alice"                     # or LITEMLFLOW_BASIC_USER
  password = "secret"                    # or LITEMLFLOW_BASIC_PASS (sensitive)
}
```

All four attributes are optional in HCL; use environment variables in CI/CD
to avoid storing credentials in state or version control.

OIDC authentication is deferred to provider v0.2.

## Resources

### `litemlflow_experiment`

```hcl
resource "litemlflow_experiment" "training" {
  name              = "production-training"
  artifact_location = "mlflow-artifacts:/training"  # optional; server assigns default
  tags = {
    team   = "ml-platform"
    domain = "search"
  }
}
```

Computed attributes: `id` (MLflow experiment ID).

CRUD maps to `/api/2.0/mlflow/experiments/*`. Tags are upserted on update
(the MLflow API does not expose a bulk-delete-tags endpoint; tags removed
from HCL are not deleted from the server in this version).

### `litemlflow_prompt`

```hcl
resource "litemlflow_prompt" "rag_system" {
  name        = "rag.system"
  content     = file("${path.module}/prompts/rag-system.txt")
  description = "Production RAG system prompt"
}
```

Computed attributes: `id` (`name@version`), `version`, `content_hash`.

Prompts are **content-addressed and append-only**: `POST /api/v1/prompts`
with identical content reuses the existing version. Changing `content`
creates a new version. The `name` attribute requires replace (destroy +
recreate) if changed. There is no in-place edit endpoint on the server.

### `litemlflow_prompt_alias`

```hcl
resource "litemlflow_prompt_alias" "rag_system_prod" {
  name    = litemlflow_prompt.rag_system.name
  alias   = "production"
  version = litemlflow_prompt.rag_system.version
}
```

An alias is a mutable pointer (e.g. `production`) to a specific version.
Changing `version` is an in-place update (upsert). Changing `name` or
`alias` requires replace.

Maps to `POST/GET/DELETE /api/v1/prompts/{name}/aliases/*`.

### `litemlflow_registered_model`

```hcl
resource "litemlflow_registered_model" "rag_retriever" {
  name        = "rag-retriever"
  description = "Production RAG retrieval model"
  tags = {
    framework = "sentence-transformers"
  }
}
```

The `name` attribute is the primary key and requires replace if changed.
Tags support full create/update/delete lifecycle.

Maps to `/api/2.0/mlflow/registered-models/*`.

### `litemlflow_workspace`

```hcl
resource "litemlflow_workspace" "team_nlp" {
  id          = "team-nlp"         # slug, immutable
  name        = "NLP Team"
  description = "Sentence embeddings and RAG work"
}
```

The `id` attribute is a slug (`[a-z0-9-]{1,64}`) and immutable after
creation (requires replace if changed). The `default` workspace cannot
be deleted via Terraform.

Maps to `/api/v1/workspaces/*`.

## Data Sources

### `data.litemlflow_experiment`

```hcl
data "litemlflow_experiment" "existing" {
  name = "production-training"
}
```

Reads an experiment by name. Returns `id`, `artifact_location`, `tags`.

### `data.litemlflow_prompt`

```hcl
# Latest version:
data "litemlflow_prompt" "rag_latest" {
  name = "rag.system"
}

# Specific version:
data "litemlflow_prompt" "rag_v2" {
  name    = "rag.system"
  version = 2
}
```

Returns `id`, `version`, `content`, `description`, `content_hash`.

## Design choices

| Decision | Rationale |
|---|---|
| HashiCorp Plugin Framework v1.x (not SDK v2) | Framework is the modern recommendation for new providers; SDK v2 is maintenance-mode |
| Separate Go module at `terraform/` | Mirrors the `operator/` pattern — no dependency coupling with the main LiteMLflow module |
| Prompts have no Update (append-only) | The server API is content-addressed; changing content always produces a new version. Terraform's `version` attribute in state tracks the current version |
| Prompt alias uses `RequiresReplace` on `name`/`alias`, not `version` | Alias identity = `(name, alias)`; re-pointing to a new version is an in-place update |
| Tags on experiments use upsert only (no delete) | MLflow compat API has `set-experiment-tag` but no `delete-experiment-tag`; registered models do have `delete-tag` and are fully synced |

## Building and testing

```bash
# From the repository root:
make terraform-build   # → bin/terraform-provider-litemlflow
make terraform-test    # → go test ./... (pure unit tests, no live server needed)
```

The test suite uses `net/http/httptest` to run an in-memory fake LiteMLflow
server. No Terraform CLI binary is required for `make terraform-test`.

For acceptance tests against a live server set `TF_ACC=1` and provide a
`TF_ACC_TERRAFORM_PATH` pointing to a Terraform 1.5+ binary.
