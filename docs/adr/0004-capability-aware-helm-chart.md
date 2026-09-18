# ADR 0004: Capability-aware Helm chart

- Status: accepted
- Date: 2026-09-18

## Context

The platform chart must install on an empty Kind cluster, on Kind with the full add-on set, and on EKS. ServiceMonitors, PrometheusRules, KEDA ScaledObjects and Istio resources fail to apply when their CRDs are absent.

## Decision

Render CRD-backed resources only when both the feature flag is on **and** `.Capabilities.APIVersions.Has` reports the API group. Without KEDA, the worker falls back to a CPU HPA. `helm template` in CI passes `--api-versions` to render every branch.

## Consequences

- `helm install titan ./charts/platform` works everywhere, in any order relative to the add-ons.
- After installing add-ons later, a `helm upgrade` (or Argo CD sync) picks up the extra resources.
- `NOTES.txt` tells the operator which optional features were rendered.
