variable "name" {
  description = "Kind cluster name."
  type        = string
  default     = "titan"
}

variable "node_image" {
  description = "kindest/node image. Empty uses the provider default."
  type        = string
  default     = ""
}

variable "worker_count" {
  description = "Number of worker nodes."
  type        = number
  default     = 3
}

variable "port_mappings" {
  description = "NodePort -> host port mappings on the control-plane node."
  type = list(object({
    container_port = number
    host_port      = number
  }))
  default = [
    { container_port = 30080, host_port = 8080 },  # NGINX edge
    { container_port = 30300, host_port = 3000 },  # Grafana
    { container_port = 30090, host_port = 9090 },  # Prometheus
    { container_port = 30686, host_port = 16686 }, # Jaeger
    { container_port = 30443, host_port = 8443 },  # Argo CD
    { container_port = 30880, host_port = 8081 },  # Jenkins
  ]
}
