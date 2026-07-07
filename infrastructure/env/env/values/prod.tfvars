# Nebula PROD account (same values stuff-ai-service uses)
account_role_arn = "arn:aws:iam::522778376395:role/nebula-admin"
environment      = "prod"

# Network facts resolve from the cluster state; OIDC issuer per ai-service tfvars.
cluster_state_key = "prod/stuff-applications.tfstate"
oidc_issuer_url   = "oidc.eks.ap-southeast-2.amazonaws.com/id/87A426F118EC170C4E0CEEA2CFB7BDC9"

# Same namespace tier as stuff-ai-service prod.
namespace = "stuff-web-and-apps"

# Prod sizing.
backup_retention_days = 14
