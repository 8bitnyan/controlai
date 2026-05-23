output "instance_id" {
  value = aws_instance.controlai.id
}

output "public_ip" {
  value = var.enable_eip ? aws_eip.controlai[0].public_ip : aws_instance.controlai.public_ip
}

output "public_dns" {
  value = aws_instance.controlai.public_dns
}

output "security_group_id" {
  value = aws_security_group.controlai.id
}

output "iam_role_arn" {
  value = aws_iam_role.controlai.arn
}

output "ami_id" {
  value = data.aws_ami.ubuntu.id
}

output "subnet_id" {
  value = local.subnet_id
}

output "availability_zone" {
  value = aws_instance.controlai.availability_zone
}
