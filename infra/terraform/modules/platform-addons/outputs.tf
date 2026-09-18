output "namespaces" {
  description = "Namespaces managed by this module."
  value       = keys(kubernetes_namespace_v1.this)
}

output "releases" {
  description = "Installed Helm releases."
  value = compact([
    one(helm_release.metrics_server[*].name),
    one(helm_release.istiod[*].name),
    helm_release.kube_prometheus_stack.name,
    helm_release.keda.name,
    helm_release.loki.name,
    helm_release.fluent_bit.name,
    helm_release.otel_collector.name,
    helm_release.jaeger.name,
    one(helm_release.argocd[*].name),
    one(helm_release.jenkins[*].name),
  ])
}
