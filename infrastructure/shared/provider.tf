provider "aws" {
  region = "ap-southeast-2"
  assume_role {
    session_name = "aws_role_assumed"
    role_arn     = var.aws_role_arn
    duration     = "1h"
  }
}

terraform {
  backend "s3" {
    bucket         = "stuff-terraform-nebula"
    region         = "ap-southeast-2"
    key            = "shared/stuff-expo-ota.tfstate"
    dynamodb_table = "stuff-terraform-nebula"
  }
}
