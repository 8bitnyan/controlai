resource "aws_eip" "controlai" {
  count = var.enable_eip ? 1 : 0
}

resource "aws_eip_association" "controlai" {
  count         = var.enable_eip ? 1 : 0
  instance_id   = aws_instance.controlai.id
  allocation_id = aws_eip.controlai[0].id
}
