data "aws_iam_policy_document" "ec2_assume_role" {
  statement {
    effect = "Allow"

    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }

    actions = ["sts:AssumeRole"]
  }
}

resource "aws_iam_role" "controlai" {
  name               = "${var.deployment_name}-role"
  assume_role_policy = data.aws_iam_policy_document.ec2_assume_role.json
}

data "aws_iam_policy_document" "ssm_get_parameter" {
  statement {
    effect    = "Allow"
    actions   = ["ssm:GetParameter"]
    resources = [var.ca_key_ssm_parameter_arn]
  }
}

resource "aws_iam_role_policy" "controlai_ssm" {
  name   = "${var.deployment_name}-ssm-get-parameter"
  role   = aws_iam_role.controlai.id
  policy = data.aws_iam_policy_document.ssm_get_parameter.json
}

resource "aws_iam_instance_profile" "controlai" {
  name = "${var.deployment_name}-instance-profile"
  role = aws_iam_role.controlai.name
}
