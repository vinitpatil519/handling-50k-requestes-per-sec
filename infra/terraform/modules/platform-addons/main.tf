# Cluster add-ons shared by the Kind and EKS environments. Mirrors
# scripts/install-addons.sh so both paths produce the same cluster.

locals {
  namespaces = {
    (var.app_namespace) = merge(
      {
        "pod-security.kubernetes.io/enforce" = "baseline"
        "pod-security.kubernetes.io/warn"    = "restricted"
      },
      var.enable_istio ? { "istio-injection" = "enabled" } : {}
    )
    titanload     = { "pod-security.kubernetes.io/enforce" = "restricted" }
    monitoring    = { "pod-security.kubernetes.io/enforce" = "privileged" }
    observability = { "pod-security.kubernetes.io/enforce" = "privileged" }
    keda          = {}
    argocd        = {}
    jenkins       = {}
    istio-system  = {}
  }

  values = { for k, v in var.extra_values : k => v }
}

resource "kubernetes_namespace_v1" "this" {
  for_each = local.namespaces

  metadata {
    name   = each.key
    labels = merge({ "app.kubernetes.io/part-of" = "titanedge" }, each.value)
  }
}

resource "helm_release" "metrics_server" {
  count = var.enable_metrics_server ? 1 : 0

  name       = "metrics-server"
  repository = "https://kubernetes-sigs.github.io/metrics-server"
  chart      = "metrics-server"
  version    = var.versions.metrics_server
  namespace  = "kube-system"
  values     = concat([file("${var.values_dir}/metrics-server.yaml")], lookup(local.values, "metrics-server", []))
}

resource "helm_release" "istio_base" {
  count = var.enable_istio ? 1 : 0

  name       = "istio-base"
  repository = "https://istio-release.storage.googleapis.com/charts"
  chart      = "base"
  version    = var.versions.istio
  namespace  = kubernetes_namespace_v1.this["istio-system"].metadata[0].name
  set        = [{ name = "defaultRevision", value = "default" }]
}

resource "helm_release" "istiod" {
  count = var.enable_istio ? 1 : 0

  name       = "istiod"
  repository = "https://istio-release.storage.googleapis.com/charts"
  chart      = "istiod"
  version    = var.versions.istio
  namespace  = kubernetes_namespace_v1.this["istio-system"].metadata[0].name
  values     = concat([file("${var.values_dir}/istiod.yaml")], lookup(local.values, "istiod", []))
  depends_on = [helm_release.istio_base]
}

resource "helm_release" "kube_prometheus_stack" {
  name       = "kube-prometheus-stack"
  repository = "https://prometheus-community.github.io/helm-charts"
  chart      = "kube-prometheus-stack"
  version    = var.versions.kube_prometheus_stack
  namespace  = kubernetes_namespace_v1.this["monitoring"].metadata[0].name
  values     = concat([file("${var.values_dir}/kube-prometheus-stack.yaml")], lookup(local.values, "kube-prometheus-stack", []))
  timeout    = 900
}

resource "helm_release" "keda" {
  name       = "keda"
  repository = "https://kedacore.github.io/charts"
  chart      = "keda"
  version    = var.versions.keda
  namespace  = kubernetes_namespace_v1.this["keda"].metadata[0].name
  values     = concat([file("${var.values_dir}/keda.yaml")], lookup(local.values, "keda", []))
  depends_on = [helm_release.kube_prometheus_stack] # ServiceMonitor CRD
}

resource "helm_release" "loki" {
  name       = "loki"
  repository = "https://grafana.github.io/helm-charts"
  chart      = "loki"
  version    = var.versions.loki
  namespace  = kubernetes_namespace_v1.this["observability"].metadata[0].name
  values     = concat([file("${var.values_dir}/loki.yaml")], lookup(local.values, "loki", []))
  timeout    = 900
}

resource "helm_release" "fluent_bit" {
  name       = "fluent-bit"
  repository = "https://fluent.github.io/helm-charts"
  chart      = "fluent-bit"
  version    = var.versions.fluent_bit
  namespace  = kubernetes_namespace_v1.this["observability"].metadata[0].name
  values     = concat([file("${var.values_dir}/fluent-bit.yaml")], lookup(local.values, "fluent-bit", []))
  depends_on = [helm_release.loki, helm_release.kube_prometheus_stack]
}

resource "helm_release" "otel_collector" {
  name       = "otel-collector"
  repository = "https://open-telemetry.github.io/opentelemetry-helm-charts"
  chart      = "opentelemetry-collector"
  version    = var.versions.otel_collector
  namespace  = kubernetes_namespace_v1.this["observability"].metadata[0].name
  values     = concat([file("${var.values_dir}/otel-collector.yaml")], lookup(local.values, "otel-collector", []))
  depends_on = [helm_release.kube_prometheus_stack]
}

resource "helm_release" "jaeger" {
  name      = "jaeger"
  chart     = "${var.charts_dir}/jaeger"
  namespace = kubernetes_namespace_v1.this["observability"].metadata[0].name
  values    = lookup(local.values, "jaeger", [])
}

resource "helm_release" "argocd" {
  count = var.enable_argocd ? 1 : 0

  name       = "argocd"
  repository = "https://argoproj.github.io/argo-helm"
  chart      = "argo-cd"
  version    = var.versions.argocd
  namespace  = kubernetes_namespace_v1.this["argocd"].metadata[0].name
  values     = concat([file("${var.values_dir}/argocd.yaml")], lookup(local.values, "argocd", []))
}

resource "helm_release" "jenkins" {
  count = var.enable_jenkins ? 1 : 0

  name       = "jenkins"
  repository = "https://charts.jenkins.io"
  chart      = "jenkins"
  version    = var.versions.jenkins
  namespace  = kubernetes_namespace_v1.this["jenkins"].metadata[0].name
  values     = concat([file("${var.values_dir}/jenkins.yaml")], lookup(local.values, "jenkins", []))
  timeout    = 900
}
