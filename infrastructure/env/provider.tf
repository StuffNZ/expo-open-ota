provider "aws" {
  region = "ap-southeast-2"
  assume_role {
    session_name = "tf_aws_role_assumed"
    role_arn     = var.account_role_arn
  }
}

terraform {
  required_version = ">= 1.5.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 6.36.0, < 7.0.0"
    }
  }

  # Backend key is per-environment, supplied by the pipeline:
  #   terraform init -backend-config=env/backend/<env>.tfbackend
  backend "s3" {
    bucket       = "stuff-terraform-nebula"
    region       = "ap-southeast-2"
    use_lockfile = true
  }
}
