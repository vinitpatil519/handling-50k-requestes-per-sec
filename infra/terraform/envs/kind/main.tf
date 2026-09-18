# Free local environment: Kind cluster + local registry + add-ons (+ optionally
# the platform chart itself) in one `terraform apply`.
#
#   terraform -chdir=infra/terraform/envs/kind init
#   terraform -chdir=infra/terraform/envs/kind apply -var profile=lite
#
# Build/push images before enabling deploy_platform (scripts/build-images.sh).

locals {
  repo_root = abspath("${path.root}/../../../..")
  full      = var.profile == "full"
}

module "cluster" {
  source       = "../../modules/kind-cluster"
  name         = var.cluster_name
  node_image   = var.node_image
  worker_count = var.worker_count
}

# Local registry + containerd wiring is Docker plumbing Terraform has no
# native resource for; reuse the idempotent script.
resource "terraform_data" "registry" {
  triggers_replace = [module.cluster.endpoint]

  provisioner "local-exec" {
    command     = "bash scripts/kind-registry.sh"
    working_dir = local.repo_root
    environment = {
      CLUSTER_NAME = var.cluster_name
      KUBECONFIG   = module.cluster.kubeconfig_path
    }
  }
}

module "addons" {
  source         = "../../modules/platform-addons"
  values_dir     = "${local.repo_root}/addons/values"
  charts_dir     = "${local.repo_root}/addons/charts"
  enable_istio   = local.full
  enable_argocd  = local.full
  enable_jenkins = local.full

  depends_on = [terraform_data.registry]
}

resource "helm_release" "platform" {
  count = var.deploy_platform ? 1 : 0

  name      = "titan"
  chart     = "${local.repo_root}/charts/platform"
  namespace = "titanedge"
  values    = [file("${local.repo_root}/environments/${local.full ? "kind-full" : "kind"}/values.yaml")]
  set       = [{ name = "global.imageTag", value = var.image_tag }]
  timeout   = 900

  depends_on = [module.addons]
}
