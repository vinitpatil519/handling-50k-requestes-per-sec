# Optional AWS path: VPC + EKS + ECR (+ RDS/ElastiCache) + the same add-ons as
# Kind. THIS COSTS MONEY - see docs/eks-deployment.md for a cost breakdown and
# always `terraform destroy` test stacks.

locals {
  repo_root = abspath("${path.root}/../../../..")
  tags = merge(var.tags, {
    Project     = "titanedge"
    Environment = var.environment
    ManagedBy   = "terraform"
  })
}

module "vpc" {
  source             = "../../modules/vpc"
  name               = var.name
  cluster_name       = var.name
  region             = var.region
  cidr               = var.vpc_cidr
  single_nat_gateway = var.single_nat_gateway
  tags               = local.tags
}

module "ecr" {
  source       = "../../modules/ecr"
  force_delete = var.environment != "prod"
  tags         = local.tags
}

module "eks" {
  source               = "../../modules/eks"
  name                 = var.name
  kubernetes_version   = var.kubernetes_version
  subnet_ids           = module.vpc.private_subnet_ids
  public_access_cidrs  = var.api_allowed_cidrs
  admin_principal_arns = var.admin_principal_arns
  tags                 = local.tags

  node_groups = {
    # Control-plane-ish add-ons: Prometheus, Loki, Argo CD, Istio.
    system = {
      instance_types = ["m7i.xlarge"]
      min_size       = 2
      max_size       = 4
      desired_size   = 3
    }
    # API / Envoy / NGINX. Compute optimised, Spot for cost.
    app = {
      instance_types = ["c7i.2xlarge", "c6i.2xlarge", "c7a.2xlarge"]
      capacity_type  = "SPOT"
      min_size       = 2
      max_size       = var.app_max_nodes
      desired_size   = 3
    }
    # Dedicated titanload workers so load generation never steals CPU from
    # the system under test.
    loadgen = {
      instance_types = ["c7i.4xlarge", "c6i.4xlarge"]
      capacity_type  = "SPOT"
      min_size       = 0
      max_size       = var.loadgen_max_nodes
      desired_size   = 0
      taints         = [{ key = "dedicated", value = "loadgen", effect = "NO_SCHEDULE" }]
    }
  }
}

module "rds" {
  count                      = var.managed_datastores ? 1 : 0
  source                     = "../../modules/rds"
  name                       = "${var.name}-pg"
  vpc_id                     = module.vpc.vpc_id
  subnet_ids                 = module.vpc.private_subnet_ids
  allowed_security_group_ids = [module.eks.cluster_security_group_id]
  deletion_protection        = var.environment == "prod"
  multi_az                   = var.environment == "prod"
  tags                       = local.tags
}

module "elasticache" {
  count                      = var.managed_datastores ? 1 : 0
  source                     = "../../modules/elasticache"
  name                       = "${var.name}-redis"
  vpc_id                     = module.vpc.vpc_id
  subnet_ids                 = module.vpc.private_subnet_ids
  allowed_security_group_ids = [module.eks.cluster_security_group_id]
  tags                       = local.tags
}

# ------------------------------------------------------------------ cluster add-ons
resource "kubernetes_storage_class_v1" "gp3" {
  metadata {
    name        = "gp3"
    annotations = { "storageclass.kubernetes.io/is-default-class" = "true" }
  }
  storage_provisioner    = "ebs.csi.aws.com"
  reclaim_policy         = "Delete"
  volume_binding_mode    = "WaitForFirstConsumer"
  allow_volume_expansion = true
  parameters             = { type = "gp3", encrypted = "true" }

  depends_on = [module.eks]
}

# AWS Load Balancer Controller: provisions the NLB in front of NGINX.
data "http" "lbc_policy" {
  url = "https://raw.githubusercontent.com/kubernetes-sigs/aws-load-balancer-controller/${var.lbc_version}/docs/install/iam_policy.json"
}

resource "aws_iam_role" "lbc" {
  name = "${var.name}-aws-lb-controller"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "pods.eks.amazonaws.com" }
      Action    = ["sts:AssumeRole", "sts:TagSession"]
    }]
  })
  tags = local.tags
}

resource "aws_iam_role_policy" "lbc" {
  name   = "aws-load-balancer-controller"
  role   = aws_iam_role.lbc.id
  policy = data.http.lbc_policy.response_body
}

resource "aws_eks_pod_identity_association" "lbc" {
  cluster_name    = module.eks.cluster_name
  namespace       = "kube-system"
  service_account = "aws-load-balancer-controller"
  role_arn        = aws_iam_role.lbc.arn
}

resource "helm_release" "lbc" {
  name       = "aws-load-balancer-controller"
  repository = "https://aws.github.io/eks-charts"
  chart      = "aws-load-balancer-controller"
  namespace  = "kube-system"
  set = [
    { name = "clusterName", value = module.eks.cluster_name },
    { name = "region", value = var.region },
    { name = "vpcId", value = module.vpc.vpc_id },
    { name = "serviceAccount.name", value = "aws-load-balancer-controller" },
  ]
  depends_on = [aws_eks_pod_identity_association.lbc]
}

module "addons" {
  source         = "../../modules/platform-addons"
  values_dir     = "${local.repo_root}/addons/values"
  charts_dir     = "${local.repo_root}/addons/charts"
  enable_istio   = true
  enable_argocd  = true
  enable_jenkins = var.enable_jenkins

  # EKS overrides: real storage, ClusterIP UIs (use kubectl port-forward or
  # an internal ALB), bigger Prometheus.
  extra_values = {
    "kube-prometheus-stack" = [file("${local.repo_root}/environments/eks/addons/kube-prometheus-stack.yaml")]
    "argocd"                = [yamlencode({ server = { service = { type = "ClusterIP" } } })]
    "jenkins"               = [yamlencode({ controller = { serviceType = "ClusterIP" } })]
    "jaeger"                = [yamlencode({ ui = { serviceType = "ClusterIP" } })]
  }

  depends_on = [kubernetes_storage_class_v1.gp3, helm_release.lbc]
}
