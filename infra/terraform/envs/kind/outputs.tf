output "kubeconfig_path" {
  value = module.cluster.kubeconfig_path
}

output "addons" {
  value = module.addons.releases
}

output "urls" {
  value = {
    app        = "http://localhost:8080"
    grafana    = "http://localhost:3000"
    prometheus = "http://localhost:9090"
    jaeger     = "http://localhost:16686"
    argocd     = var.profile == "full" ? "https://localhost:8443" : null
    jenkins    = var.profile == "full" ? "http://localhost:8081" : null
  }
}
