# ADR 0003: Build titanload instead of using an existing tool

- Status: accepted
- Date: 2026-09-18

## Context

The project needs a load generator that speaks HTTP/1.1, h2c and gRPC, shows live RPS and tail latency, runs distributed with exact aggregate percentiles, exports Prometheus metrics for side-by-side dashboards, and gates CI on SLOs - from one static binary that runs on Windows, Linux, in pods and on VMs.

## Decision

Write `titanload` in Go, sharing the repository's toolchain:

- Log-linear histogram (2% buckets, 1 µs-60 s) written through per-goroutine atomic shards; histograms merge by addition, so distributed percentiles are exact.
- Open-model pacing that stamps each request with its scheduled time, avoiding coordinated omission.
- A small JSON-over-HTTP coordinator/worker protocol with a shared token.
- ANSI dashboard without a TUI framework; plain output when not on a TTY.

## Consequences

- One more component to maintain, covered by unit tests (histogram accuracy, exact request counts, rate accuracy, end-to-end distributed aggregation).
- Tight integration: Helm chart, Ansible role, Grafana dashboard and Jenkins gate all use the same binary.
