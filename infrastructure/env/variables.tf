variable "account_role_arn" {
  description = "nebula-admin role in the target environment's AWS account"
  type        = string
}

variable "environment" {
  description = "nonprod | prod — suffixes every resource name and secret path"
  type        = string
  validation {
    condition     = contains(["nonprod", "prod"], var.environment)
    error_message = "environment must be nonprod or prod"
  }
}

variable "cluster_state_key" {
  description = "Terraform state key of the applications EKS cluster (network facts come from its outputs)"
  type        = string
}

variable "oidc_issuer_url" {
  description = "The environment cluster's OIDC issuer (for IRSA), as in stuff-ai-service tfvars"
  type        = string
}

variable "namespace" {
  description = "Kubernetes namespace the OTA server deploys into (same as ai-service's for the tier)"
  type        = string
}

variable "service_account_name" {
  description = "Service account created by deployment-basic; bound to the IRSA role"
  type        = string
  default     = "stuff-expo-ota"
}

variable "db_username" {
  type    = string
  default = "expo_ota"
}

variable "rds_engine_version" {
  type    = string
  default = "17.9"
}

variable "rds_instance_class" {
  type    = string
  default = "db.t4g.small"
}

variable "backup_retention_days" {
  type    = number
  default = 7
}
