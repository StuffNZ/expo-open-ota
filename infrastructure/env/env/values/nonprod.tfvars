# Nebula NON-PROD account (same values stuff-ai-service uses)
account_role_arn = "arn:aws:iam::781247136068:role/nebula-admin"
environment      = "nonprod"

# Network facts resolve from the cluster state; OIDC issuer per ai-service tfvars.
cluster_state_key = "staging/stuff-applications.tfstate"
oidc_issuer_url   = "oidc.eks.ap-southeast-2.amazonaws.com/id/FF9838D39063B4903A4E6092511D003E"

# Same namespace tier as stuff-ai-service staging.
namespace = "stuff-web-and-apps-staging"
