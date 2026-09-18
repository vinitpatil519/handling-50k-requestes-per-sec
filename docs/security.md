# Security model

## Workloads

| Control | Where |
|---|---|
| Distroless, non-root (uid 65532) Go images; no shell | [build/go.Dockerfile](../build/go.Dockerfile) |
| `runAsNonRoot`, `seccompProfile: RuntimeDefault`, `readOnlyRootFilesystem`, `allowPrivilegeEscalation: false`, all capabilities dropped | chart `podSecurityContext` / `containerSecurityContext` |
| Pod Security Admission: `baseline` enforced, `restricted` audited/warned (`titanload` enforces `restricted`) | [k8s/namespaces.yaml](../k8s/namespaces.yaml) |
| No Kubernetes API tokens mounted (`automountServiceAccountToken: false`), one ServiceAccount per workload | [serviceaccounts.yaml](../charts/platform/templates/serviceaccounts.yaml) |
| Resource requests/limits on every container; PDBs on every tier | chart |
| Image scanning (Trivy) blocks fixable HIGH/CRITICAL in CI; ECR scan on push; immutable tags | [Jenkinsfile](../Jenkinsfile), [ecr module](../infra/terraform/modules/ecr) |

## Network

- **Default-deny ingress** in `titanedge`, then one NetworkPolicy per hop: anything → NGINX:8080; NGINX/web/titanload → Envoy; Envoy/titanload → API; API/worker/migrate → PostgreSQL, Redis; API/worker/KEDA → Kafka; `monitoring` → metrics ports only. [networkpolicies.yaml](../charts/platform/templates/networkpolicies.yaml)
- **Istio STRICT mTLS** for all sidecar traffic, with PERMISSIVE exceptions only for the public NGINX port and Prometheus scrape ports. [istio.yaml](../charts/platform/templates/istio.yaml)
- **AuthorizationPolicies** by SPIFFE identity: only Envoy's service account (and the load-test namespace) may call the API; only NGINX/web may call Envoy.
- Metrics are on a separate port and never routed by the edge; Envoy admin is not exposed outside the cluster.
- Data stores are outside the mesh; on EKS they are managed services in private subnets reachable only from the cluster security group, with TLS (ElastiCache) and `sslmode=require` (RDS).

## Application

- Request bodies capped at 1 MiB; unknown JSON fields rejected; strict input validation (event type regex, name length, JSON validity).
- Admission control (`MAX_INFLIGHT`) and Envoy local rate limiting protect against overload; NGINX caps body size and uses sane timeouts.
- Panics are recovered and logged without leaking internals; errors returned to clients are generic.
- Security headers on the web tier (`X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`); `X-Powered-By` disabled.

## Secrets

- Kind: dev defaults in `values.yaml` (clearly marked). Change them or set `postgres.auth.existingSecret`.
- EKS: RDS generates and stores the master password in Secrets Manager (never in Terraform state); Kubernetes Secrets are envelope-encrypted with a customer-managed KMS key; workload IAM uses EKS Pod Identity (no static keys).
- titanload coordinator/worker traffic is authenticated with a shared token (`TITANLOAD_TOKEN`).

## Access

- EKS uses access entries (`authentication_mode = API`), not the `aws-auth` ConfigMap.
- Chart RBAC: `titanedge:oncall` (read-only) and `titanedge:operators` (restart/scale) groups. [rbac.yaml](../charts/platform/templates/rbac.yaml)
- Jenkins has namespace-scoped roles only; Argo CD project roles separate `deployer` (sync) from `readonly`.

## Known dev-only shortcuts

| Shortcut | Production alternative |
|---|---|
| Grafana anonymous viewer, `admin/admin` | SSO (OIDC) and a secret-managed admin password |
| Jenkins `admin/admin` | SSO + credentials from a secret store |
| Plaintext Kafka/Redis/PostgreSQL inside Kind | TLS listeners / managed services with TLS |
| Local registry over HTTP | ECR (TLS, IAM) |
| `ingress.enabled=false`, NodePort edge | NLB/ALB with TLS certificates (ACM) and WAF |
