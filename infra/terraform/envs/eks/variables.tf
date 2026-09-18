variable "name" {
  description = "Cluster / resource name prefix."
  type        = string
  default     = "titanedge"
}

variable "environment" {
  description = "dev | staging | prod (prod enables deletion protection and Multi-AZ)."
  type        = string
  default     = "dev"
}

variable "region" {
  type    = string
  default = "us-east-1"
}

variable "kubernetes_version" {
  type    = string
  default = "1.35"
}

variable "vpc_cidr" {
  type    = string
  default = "10.42.0.0/16"
}

variable "single_nat_gateway" {
  description = "Cheaper single NAT; set false for production."
  type        = bool
  default     = true
}

variable "api_allowed_cidrs" {
  description = "CIDRs allowed to reach the Kubernetes API. Restrict to your office/VPN."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "admin_principal_arns" {
  description = "Extra IAM principals with cluster-admin (the creator is added automatically)."
  type        = list(string)
  default     = []
}

variable "app_max_nodes" {
  type    = number
  default = 30
}

variable "loadgen_max_nodes" {
  type    = number
  default = 20
}

variable "managed_datastores" {
  description = "Create RDS PostgreSQL + ElastiCache instead of in-cluster data stores."
  type        = bool
  default     = true
}

variable "enable_jenkins" {
  type    = bool
  default = false
}

variable "lbc_version" {
  description = "AWS Load Balancer Controller git tag used for its IAM policy."
  type        = string
  default     = "v2.13.3"
}

variable "tags" {
  type    = map(string)
  default = {}
}
