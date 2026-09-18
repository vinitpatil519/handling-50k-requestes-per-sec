variable "values_dir" {
  description = "Directory holding the add-on values files (addons/values)."
  type        = string
}

variable "charts_dir" {
  description = "Directory holding local add-on charts (addons/charts)."
  type        = string
}

variable "app_namespace" {
  description = "Namespace of the TitanEdge platform."
  type        = string
  default     = "titanedge"
}

variable "enable_istio" {
  description = "Install Istio (base + istiod) and label the app namespace for sidecar injection."
  type        = bool
  default     = false
}

variable "enable_argocd" {
  description = "Install Argo CD."
  type        = bool
  default     = false
}

variable "enable_jenkins" {
  description = "Install Jenkins."
  type        = bool
  default     = false
}

variable "enable_metrics_server" {
  description = "Install metrics-server (EKS clusters may run it as an EKS add-on instead)."
  type        = bool
  default     = true
}

variable "extra_values" {
  description = "Per-release YAML documents applied after the repository values, keyed by release name."
  type        = map(list(string))
  default     = {}
}

variable "versions" {
  description = "Helm chart versions (keep in sync with addons/versions.env)."
  type = object({
    metrics_server        = string
    istio                 = string
    kube_prometheus_stack = string
    loki                  = string
    fluent_bit            = string
    otel_collector        = string
    keda                  = string
    argocd                = string
    jenkins               = string
  })
  default = {
    metrics_server        = "3.14.0"
    istio                 = "1.31.0"
    kube_prometheus_stack = "91.4.1"
    loki                  = "7.3.0"
    fluent_bit            = "0.58.2"
    otel_collector        = "0.173.1"
    keda                  = "2.20.2"
    argocd                = "10.9.2"
    jenkins               = "5.9.63"
  }
}
