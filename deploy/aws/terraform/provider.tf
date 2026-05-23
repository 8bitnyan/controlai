locals {
  base_tags = {
    Project        = "controlai"
    ManagedBy      = "controlai-aws-provisioning"
    Environment    = var.deployment_name
    DeploymentName = var.deployment_name
  }
}

provider "aws" {
  region = var.aws_region

  default_tags {
    tags = merge(local.base_tags, var.extra_tags)
  }
}
