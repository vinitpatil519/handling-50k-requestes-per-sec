# ADR 0002: franz-go as the Kafka client

- Status: accepted
- Date: 2026-09-18

## Context

The cluster runs Kafka 4.x in KRaft mode. Kafka 4.0 removed many old protocol versions (KIP-896). The API needs a non-blocking, batching producer with backpressure; the worker needs manual offset commits and rebalance control for at-least-once aggregation.

## Decision

Use `github.com/twmb/franz-go` (pure Go, no cgo): `TryProduce` + `MaxBufferedRecords` for non-blocking produce with a bounded buffer, `kadm` for idempotent topic creation, and `DisableAutoCommit` + `BlockRebalanceOnPoll` in the worker so offsets are committed only after a batch is persisted.

## Consequences

- Static binaries and distroless images (no librdkafka).
- Full protocol support for current brokers and MSK.
- Backpressure surfaces as HTTP 503 instead of memory growth.
