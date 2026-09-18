# ADR 0001: NGINX in front of Envoy

- Status: accepted
- Date: 2026-09-18

## Context

The edge must hold very large numbers of client connections, serve the dashboard's static assets, and balance HTTP/2 traffic evenly across API pods that scale up and down every few seconds. kube-proxy balances per TCP connection, which pins long-lived HTTP/2 connections to whichever pods existed when they opened.

## Decision

NGINX terminates client connections and routes `/` to Next.js and `/api/v1` to Envoy over a keep-alive pool. Envoy discovers every API pod IP through a headless Service (`STRICT_DNS`) and balances **per request** with `LEAST_REQUEST`, adding outlier detection, retry budgets, circuit breakers and local rate limiting.

The upstream ingress-nginx controller was retired by Kubernetes SIG Network, so NGINX runs as a plain Deployment with a ConfigMap-managed config, exposed by a NodePort on Kind and an NLB on EKS. An optional `Ingress` object can still front it for clusters that run another controller.

## Consequences

- One extra hop (~0.1-0.3 ms) and CPU per request.
- Each proxy does what it is best at, and either can be removed without touching the API.
- New API pods receive traffic within one DNS refresh (5 s) of becoming ready.
