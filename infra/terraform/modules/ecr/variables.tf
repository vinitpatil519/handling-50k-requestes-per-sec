variable "prefix" {
  description = "Repository namespace, e.g. titanedge -> titanedge/api."
  type        = string
  default     = "titanedge"
}

variable "repositories" {
  description = "Image names."
  type        = list(string)
  default     = ["api", "worker", "web", "titanload", "ci-tools"]
}

variable "keep_images" {
  description = "Number of images retained per repository."
  type        = number
  default     = 50
}

variable "force_delete" {
  description = "Allow destroying repositories that still contain images."
  type        = bool
  default     = false
}

variable "tags" {
  description = "Tags applied to every resource."
  type        = map(string)
  default     = {}
}
