# Optional Argo CD applications

The app-of-apps in `../apps` installs the "lite" profile. To run the Istio
mesh through GitOps as well:

1. Copy `01a-istio-base.yaml` and `01b-istiod.yaml` into `../apps/`.
2. Add `istio-injection: enabled` to the `titanedge` namespace in
   `k8s/namespaces.yaml`.
3. Point `apps/20-titanedge-platform.yaml` at
   `../../environments/kind-full/values.yaml`.
4. Commit and push; Argo CD installs Istio (waves -19/-18) before the
   platform (wave 0).
