variable "aws_region" {
  type = string
}

variable "deployment_name" {
  type = string
}

variable "instance_type" {
  type    = string
  default = "t3.medium"
}

variable "enable_eip" {
  type    = bool
  default = false
}

variable "ssh_key_name" {
  type = string
}

variable "user_data" {
  type = string
}

variable "ca_key_ssm_parameter_arn" {
  type = string
}

variable "controlai_version" {
  type = string
}

variable "extra_tags" {
  type    = map(string)
  default = {}
}
