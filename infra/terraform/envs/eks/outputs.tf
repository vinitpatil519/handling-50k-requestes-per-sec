output "cluster_name" {
  value = module.eks.cluster_name
}

output "kubeconfig_command" {
  value = "aws eks update-kubeconfig --name ${module.eks.cluster_name} --region ${var.region}"
}

output "ecr_registry" {
  description = "Use as global.imageRegistry in environments/eks/values.yaml."
  value       = module.ecr.registry
}

output "rds_endpoint" {
  value = try(module.rds[0].endpoint, null)
}

output "rds_master_secret_arn" {
  description = "Build the titan-db Kubernetes secret from this (see docs/eks-deployment.md)."
  value       = try(module.rds[0].master_user_secret_arn, null)
}

output "redis_endpoint" {
  description = "externalRedis.addr for environments/eks/values.yaml."
  value       = try(module.elasticache[0].primary_endpoint, null)
}
