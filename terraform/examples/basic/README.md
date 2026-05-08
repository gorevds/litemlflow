# Basic LiteMLflow Terraform example

This example provisions a complete LiteMLflow setup:

- A workspace (`ml-platform`)
- An experiment with tags
- A versioned prompt with a production alias
- A registered model
- Data source lookups for both the experiment and the prompt

## Prerequisites

1. A running LiteMLflow server (see the main [README](../../README.md) for install instructions).
2. Terraform CLI 1.5+ installed locally (`terraform version`).
3. The provider binary installed via `dev_overrides` (see below) or published to the registry.

## Local dev_overrides install

Build the provider, then tell Terraform to use the local binary:

```bash
# From the repository root:
make terraform-build
# binary lands at bin/terraform-provider-litemlflow
```

Add a `~/.terraformrc` (or `%APPDATA%/terraform.rc` on Windows):

```hcl
provider_installation {
  dev_overrides {
    "gorevds/litemlflow" = "/path/to/repo/bin"
  }
  direct {}
}
```

## Configure

Set environment variables (recommended over hard-coding credentials in HCL):

```bash
export LITEMLFLOW_URL=https://lmf.example.com
export LITEMLFLOW_BASIC_USER=alice
export LITEMLFLOW_BASIC_PASS=secret
```

Or edit `main.tf` to set `url`, `username`, and `password` directly.

## Run

```bash
cd terraform/examples/basic
terraform init     # skip if using dev_overrides (no registry lookup)
terraform plan
terraform apply
terraform destroy
```

## What gets created

| Resource | API endpoint |
|---|---|
| `litemlflow_workspace.ml_platform` | `POST /api/v1/workspaces` |
| `litemlflow_experiment.training` | `POST /api/2.0/mlflow/experiments/create` |
| `litemlflow_prompt.rag_system` | `POST /api/v1/prompts` |
| `litemlflow_prompt_alias.rag_system_prod` | `POST /api/v1/prompts/rag.system/aliases` |
| `litemlflow_registered_model.rag_retriever` | `POST /api/2.0/mlflow/registered-models/create` |
