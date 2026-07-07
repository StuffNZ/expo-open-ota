# Per-environment infrastructure for the OTA server (run once with
# env/values/nonprod.tfvars, once with prod — the pipeline selects the
# matching backend key).

data "aws_caller_identity" "current" {}

/* Network comes from the EKS cluster state — no hardcoding in tfvars
   (same pattern as stuff-ai-service/terraform/rds). */
data "terraform_remote_state" "eks_cluster" {
  backend = "s3"
  config = {
    bucket = "stuff-terraform-nebula"
    region = "ap-southeast-2"
    key    = var.cluster_state_key
  }
}

locals {
  account_id = data.aws_caller_identity.current.account_id
  name       = "stuff-expo-ota-${var.environment}"

  vpc_id          = data.terraform_remote_state.eks_cluster.outputs.eks["vpc_id"]
  private_subnets = data.terraform_remote_state.eks_cluster.outputs.eks["private_subnets"]
  cluster_sg_id   = data.terraform_remote_state.eks_cluster.outputs.eks["EKS_Cluster_SG_ID"]
}

# ---------------------------------------------------------
# RDS master password (URL-safe: no special chars so it can
# be embedded in DB_URL without percent-encoding).
# ---------------------------------------------------------
resource "random_password" "db" {
  length  = 24
  special = false
}

# ---------------------------------------------------------
# RDS Postgres 17 — the OTA control-plane database (apps,
# channels, branches, API keys). This state is what lets
# installed apps receive updates: backups are not optional.
# ---------------------------------------------------------
module "ota_rds" {
  source = "s3::https://stuff-terraform-nebula-modules.s3.ap-southeast-2.amazonaws.com/stuff-nebula-rds/3.2.3.zip"

  vpc_id         = local.vpc_id
  subnet_ids     = local.private_subnets
  inbound_sg_ids = [local.cluster_sg_id]

  identifier          = local.name
  db_name             = "expo_ota"
  engine              = "postgres"
  engine_version      = var.rds_engine_version
  instance_class      = var.rds_instance_class
  allocated_storage   = 20
  username            = var.db_username
  rds_password        = random_password.db.result
  skip_final_snapshot = false

  backup_retention_period = var.backup_retention_days
}

# ---------------------------------------------------------
# S3 — update bundles + assets (STORAGE_MODE=s3). Versioning
# ON so a bad publish or fat-fingered delete is recoverable.
# ---------------------------------------------------------
module "updates_bucket" {
  source = "s3::https://stuff-terraform-nebula-modules.s3.ap-southeast-2.amazonaws.com/stuff-nebula-s3/1.1.0.zip"

  bucket_name = "${local.name}-updates"

  manage_versioning  = true
  versioning_enabled = true

  manage_public_access_block = true

  sse_algorithm = "AES256"
}

# ---------------------------------------------------------
# IRSA — the server pod's service account role. Used for:
#   - S3 RW on the updates bucket
#   - the app's own master-key fetch (AWSSM_* env)
#   - the deployment-basic chart's ExternalSecrets SecretStore
#     (SecretStore auth = this same service account JWT)
# ---------------------------------------------------------
module "ota_server_role" {
  source = "s3::https://stuff-terraform-nebula-modules.s3.ap-southeast-2.amazonaws.com/stuff-nebula-pod-sa-role/1.0.3.zip"

  name               = "${local.name}-server"
  environment        = var.environment
  oidc_issuer_url    = var.oidc_issuer_url
  cluster_account_id = local.account_id
  namespace          = var.namespace
  service_account    = var.service_account_name
}

resource "aws_iam_role_policy" "ota_server" {
  name = "${local.name}-server"
  role = module.ota_server_role.service_account_role.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid      = "UpdatesBucketRW"
        Effect   = "Allow"
        Action   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:ListBucket"]
        Resource = ["arn:aws:s3:::${local.name}-updates", "arn:aws:s3:::${local.name}-updates/*"]
      },
      {
        Sid    = "OwnSecretsRead"
        Effect = "Allow"
        Action = ["secretsmanager:GetSecretValue"]
        Resource = [
          aws_secretsmanager_secret.master_key.arn,
          aws_secretsmanager_secret.app.arn,
        ]
      }
    ]
  })
}

# ---------------------------------------------------------
# Secrets Manager.
# master-key: OPERATOR-SET (see deploy/README.md phase 0) —
# it seals per-app signing keys/tokens in the DB; regenerating
# it orphans them, so Terraform only creates the shell. The
# server fetches it directly via AWSSM_CONTROL_PLANE_MASTER_KEY_B64_SECRET_ID.
# ---------------------------------------------------------
resource "aws_secretsmanager_secret" "master_key" {
  name = "stuff-expo-ota/${var.environment}/master-key-b64"
}

resource "aws_secretsmanager_secret_version" "master_key_placeholder" {
  secret_id     = aws_secretsmanager_secret.master_key.id
  secret_string = "OPERATOR-MUST-SET-32-BYTE-BASE64-KEY"
  lifecycle {
    ignore_changes = [secret_string]
  }
}

resource "random_password" "admin" {
  length  = 32
  special = false
}

resource "random_password" "jwt" {
  length  = 48
  special = false
}

# App env secrets as ONE JSON secret: the deployment-basic chart's
# ExternalSecrets convention maps env keys to JSON properties of an SM secret
# (externalSecrets.properties in deploy/values-*.yaml).
# sslmode=require: RDS defaults to force_ssl so plaintext is rejected, and the
# image doesn't carry the RDS CA bundle needed for verify-full (upgrading =
# bake the CA into the image, upstream-able).
resource "aws_secretsmanager_secret" "app" {
  name = "stuff-expo-ota/${var.environment}/app"
}

resource "aws_secretsmanager_secret_version" "app" {
  secret_id = aws_secretsmanager_secret.app.id
  secret_string = jsonencode({
    DB_URL         = "postgres://${var.db_username}:${random_password.db.result}@${module.ota_rds.endpoint}/${module.ota_rds.db_name}?sslmode=require"
    ADMIN_PASSWORD = random_password.admin.result
    JWT_SECRET     = random_password.jwt.result
  })
}

output "ota_server_role_arn" {
  value = module.ota_server_role.service_account_role.arn
}

output "updates_bucket" {
  value = "${local.name}-updates"
}
