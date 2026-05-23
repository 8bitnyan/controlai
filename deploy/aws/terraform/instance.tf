resource "aws_instance" "controlai" {
  ami                         = data.aws_ami.ubuntu.id
  instance_type               = var.instance_type
  subnet_id                   = local.subnet_id
  vpc_security_group_ids      = [aws_security_group.controlai.id]
  key_name                    = var.ssh_key_name
  iam_instance_profile        = aws_iam_instance_profile.controlai.name
  user_data                   = var.user_data
  associate_public_ip_address = true

  root_block_device {
    volume_type = "gp3"
    volume_size = 50
    encrypted   = true
  }

  metadata_options {
    http_tokens   = "required"
    http_endpoint = "enabled"
  }

  tags = {
    Name = "${var.deployment_name}-host"
  }
}
