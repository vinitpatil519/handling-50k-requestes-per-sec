variable "cluster_name" {
  description = "Kind cluster name."
  type        = string
  default     = "titan"
}

variable "node_image" {
  description = "kindest/node image; empty uses the provider default."
  type        = string
  default     = ""
}

variable "worker_count" {
  description = "Worker nodes."
  type        = number
  default     = 3
}

variable "profile" {
  description = "lite (observability + KEDA) or full (+ Istio, Argo CD, Jenkins)."
  type        = string
  default     = "lite"
  validation {
    condition     = contains(["lite", "full"], var.profile)
    error_message = "profile must be lite or full."
  }
}

variable "deploy_platform" {
  description = "Also install charts/platform (push images to localhost:5001 first)."
  type        = bool
  default     = false
}

variable "image_tag" {
  description = "Tag of the titanedge/* images."
  type        = string
  default     = "dev"
}
