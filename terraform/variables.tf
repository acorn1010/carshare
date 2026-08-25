# Every secret is a variable with no default, so nothing sensitive can end up
# in this open-source repo. Provide them with -var-file or TF_VAR_ env vars.

variable "cloudflare_api_token" {
  description = "Cloudflare token with DNS edit on the zone."
  type        = string
  sensitive   = true
}

variable "cloudflare_zone_id" {
  description = "Zone id that holds the domain."
  type        = string
}

variable "domain" {
  description = "Public hostname the API serves."
  type        = string
  default     = "cars.foony.com"
}

variable "ingress_target" {
  description = "Hostname of the cluster's public ingress, used as the CNAME target."
  type        = string
}

variable "kubeconfig_path" {
  description = "Path to the kubeconfig for the target cluster."
  type        = string
}

variable "kube_context" {
  description = "Kubeconfig context to use. Empty means the current one."
  type        = string
  default     = ""
}

variable "namespace" {
  description = "Kubernetes namespace for the service."
  type        = string
  default     = "carshare"
}

variable "image" {
  description = "Container image without tag."
  type        = string
  default     = "ghcr.io/foony-limited/carshare"
}

variable "image_tag" {
  description = "Image tag to deploy. Use the immutable release tag from CI, latest only for dev."
  type        = string
  default     = "latest"
}

variable "database_url" {
  description = "Postgres connection string for the service. Leave empty to run a single-pod Postgres inside the namespace, right for a demo, point it at managed Postgres for real load."
  type        = string
  sensitive   = true
  default     = ""
}

variable "postgres_password" {
  description = "Password for the in-namespace Postgres. Required when database_url is empty."
  type        = string
  sensitive   = true
  default     = ""
}

variable "postgres_storage_gi" {
  description = "Disk for the in-namespace Postgres."
  type        = number
  default     = 20
}

variable "google_client_id" {
  description = "Google OAuth client id. Empty disables sign-in routes."
  type        = string
  default     = ""
}

variable "google_client_secret" {
  description = "Google OAuth client secret."
  type        = string
  sensitive   = true
  default     = ""
}

variable "r2_endpoint" {
  description = "R2 S3 endpoint, like https://<account>.r2.cloudflarestorage.com."
  type        = string
}

variable "r2_bucket" {
  description = "R2 bucket that receives nightly database dumps."
  type        = string
}

variable "r2_access_key_id" {
  description = "R2 access key for the backup job."
  type        = string
  sensitive   = true
}

variable "r2_secret_access_key" {
  description = "R2 secret key for the backup job."
  type        = string
  sensitive   = true
}

variable "image_pull_secret" {
  description = "Name of an existing dockerconfigjson secret in the namespace for private registries. Empty for public images."
  type        = string
  default     = ""
}

variable "enable_healthcheck" {
  description = "Create the external Cloudflare health check. Needs a token with Healthchecks Edit."
  type        = bool
  default     = true
}

variable "enable_prometheus_rule" {
  description = "Install the PrometheusRule alerts. Needs the prometheus-operator CRDs on the cluster."
  type        = bool
  default     = true
}

variable "min_replicas" {
  description = "HPA floor. Two keeps deploys and node drains invisible."
  type        = number
  default     = 2
}

variable "max_replicas" {
  description = "HPA ceiling."
  type        = number
  default     = 10
}
