variable "name" {
  description = "Instance identifier."
  type        = string
}

variable "vpc_id" {
  description = "VPC of the database."
  type        = string
}

variable "subnet_ids" {
  description = "Private subnets for the DB subnet group."
  type        = list(string)
}

variable "allowed_security_group_ids" {
  description = "Security groups allowed to connect (the EKS cluster security group)."
  type        = list(string)
}

variable "engine_major_version" {
  description = "PostgreSQL major version."
  type        = string
  default     = "17"
}

variable "instance_class" {
  description = "RDS instance class."
  type        = string
  default     = "db.r7g.large"
}

variable "allocated_storage" {
  description = "Initial storage in GiB (autoscaling up to 4x)."
  type        = number
  default     = 100
}

variable "max_connections" {
  description = "max_connections; size it against api replicas x DB_MAX_CONNS (or add RDS Proxy)."
  type        = number
  default     = 2000
}

variable "database" {
  type    = string
  default = "titan"
}

variable "username" {
  type    = string
  default = "titan"
}

variable "multi_az" {
  description = "Synchronous standby in a second AZ."
  type        = bool
  default     = true
}

variable "deletion_protection" {
  description = "Protect against accidental deletion (disable for throwaway test stacks)."
  type        = bool
  default     = true
}

variable "tags" {
  type    = map(string)
  default = {}
}
