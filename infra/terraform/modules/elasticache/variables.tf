variable "name" {
  description = "Replication group id."
  type        = string
}

variable "vpc_id" {
  type = string
}

variable "subnet_ids" {
  type = list(string)
}

variable "allowed_security_group_ids" {
  description = "Security groups allowed to connect (the EKS cluster security group)."
  type        = list(string)
}

variable "engine_version" {
  type    = string
  default = "7.1"
}

variable "node_type" {
  type    = string
  default = "cache.r7g.large"
}

variable "replicas" {
  description = "Read replicas in addition to the primary."
  type        = number
  default     = 1
}

variable "tags" {
  type    = map(string)
  default = {}
}
