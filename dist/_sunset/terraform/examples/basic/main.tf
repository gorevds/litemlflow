terraform {
  required_providers {
    litemlflow = {
      source  = "gorevds/litemlflow"
      version = "~> 0.1"
    }
  }
}

provider "litemlflow" {
  url      = "https://lmf.example.com" # or env LITEMLFLOW_URL
  auth     = "basic"                   # or env LITEMLFLOW_AUTH
  username = "alice"                   # env LITEMLFLOW_BASIC_USER
  password = "..."                     # env LITEMLFLOW_BASIC_PASS
}

# Workspace for the ML platform team.
resource "litemlflow_workspace" "ml_platform" {
  id          = "ml-platform"
  name        = "ML Platform"
  description = "Workspace for the ML platform team"
}

# Experiment inside the default workspace.
resource "litemlflow_experiment" "training" {
  name              = "production-training"
  artifact_location = "mlflow-artifacts:/training"
  tags = {
    team   = "ml-platform"
    domain = "search"
  }
}

# Versioned RAG system prompt.
resource "litemlflow_prompt" "rag_system" {
  name        = "rag.system"
  content     = "You are a helpful assistant specialising in retrieval-augmented generation."
  description = "Production RAG system prompt"
}

# Alias pointing to the currently deployed version.
resource "litemlflow_prompt_alias" "rag_system_prod" {
  name    = litemlflow_prompt.rag_system.name
  alias   = "production"
  version = litemlflow_prompt.rag_system.version
}

# Registered model.
resource "litemlflow_registered_model" "rag_retriever" {
  name        = "rag-retriever"
  description = "Production RAG retrieval model"
  tags = {
    framework = "sentence-transformers"
    team      = "ml-platform"
  }
}

# Data source: look up an experiment by name.
data "litemlflow_experiment" "default_training" {
  name = litemlflow_experiment.training.name
  depends_on = [litemlflow_experiment.training]
}

# Data source: look up the latest prompt version.
data "litemlflow_prompt" "rag_latest" {
  name       = litemlflow_prompt.rag_system.name
  depends_on = [litemlflow_prompt.rag_system]
}

output "experiment_id" {
  description = "The MLflow experiment ID for production-training."
  value       = litemlflow_experiment.training.id
}

output "prompt_version" {
  description = "The deployed version of the RAG system prompt."
  value       = litemlflow_prompt.rag_system.version
}

output "prompt_content_hash" {
  description = "SHA-256 content hash of the deployed prompt."
  value       = litemlflow_prompt.rag_system.content_hash
}
