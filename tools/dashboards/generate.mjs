#!/usr/bin/env node
// Generates the Grafana dashboards in charts/platform/dashboards/.
// Dashboards are code: edit this file, run `node tools/dashboards/generate.mjs`
// (or `make dashboards`) and commit the JSON output.
import { writeFileSync, mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const OUT = join(dirname(fileURLToPath(import.meta.url)), "../../charts/platform/dashboards");
const PROM = { type: "prometheus", uid: "prometheus" };
const LOKI = { type: "loki", uid: "loki" };
const NS = 'namespace=~"$namespace"';

let nextId = 1;
const ts = (title, targets, { unit = "short", w = 12, h = 8, stack = false, min = 0, desc } = {}) => ({
  type: "timeseries",
  title,
  description: desc,
  datasource: PROM,
  gridPos: { w, h },
  fieldConfig: {
    defaults: {
      unit,
      min,
      custom: { lineWidth: 2, fillOpacity: stack ? 30 : 10, stacking: { mode: stack ? "normal" : "none" }, showPoints: "never" },
    },
    overrides: [],
  },
  options: { legend: { displayMode: "table", placement: "bottom", calcs: ["lastNotNull", "max"] }, tooltip: { mode: "multi" } },
  targets: targets.map(([expr, legendFormat], i) => ({ datasource: PROM, expr, legendFormat, refId: String.fromCharCode(65 + i) })),
});

const stat = (title, expr, { unit = "short", w = 4, h = 4, thresholds, decimals } = {}) => ({
  type: "stat",
  title,
  datasource: PROM,
  gridPos: { w, h },
  fieldConfig: {
    defaults: {
      unit,
      decimals,
      color: { mode: "thresholds" },
      thresholds: { mode: "absolute", steps: thresholds ?? [{ color: "green", value: null }] },
    },
    overrides: [],
  },
  options: { reduceOptions: { calcs: ["lastNotNull"] }, colorMode: "background", graphMode: "area", textMode: "value" },
  targets: [{ datasource: PROM, expr, refId: "A", instant: false }],
});

const logs = (title, expr, { w = 24, h = 12 } = {}) => ({
  type: "logs",
  title,
  datasource: LOKI,
  gridPos: { w, h },
  options: { showTime: true, wrapLogMessage: true, sortOrder: "Descending", enableLogDetails: true, prettifyLogMessage: false },
  targets: [{ datasource: LOKI, expr, refId: "A" }],
});

const row = (title) => ({ type: "row", title, collapsed: false, gridPos: { w: 24, h: 1 }, panels: [] });

// Lays panels out left-to-right, wrapping at 24 columns.
function layout(panels) {
  let x = 0, y = 0, rowH = 0;
  for (const p of panels) {
    p.id = nextId++;
    const { w, h } = p.gridPos;
    if (p.type === "row" || x + w > 24) { y += rowH; x = 0; rowH = 0; }
    p.gridPos = { x, y, w, h };
    x += w; rowH = Math.max(rowH, h);
    if (p.type === "row") { y += h; x = 0; rowH = 0; }
  }
  return panels;
}

const nsVar = (metric) => ({
  name: "namespace",
  label: "Namespace",
  type: "query",
  datasource: PROM,
  query: { query: `label_values(${metric}, namespace)`, refId: "ns" },
  definition: `label_values(${metric}, namespace)`,
  includeAll: true,
  allValue: ".*",
  multi: true,
  current: { selected: true, text: ["All"], value: ["$__all"] },
  refresh: 2,
  sort: 1,
});

function dashboard(uid, title, panels, { vars = [], tags = [], refresh = "10s", from = "now-30m" } = {}) {
  nextId = 1;
  return {
    uid,
    title,
    tags: ["titanedge", ...tags],
    timezone: "browser",
    editable: true,
    graphTooltip: 1,
    refresh,
    schemaVersion: 39,
    version: 1,
    time: { from, to: "now" },
    templating: { list: vars },
    annotations: { list: [] },
    links: [{ title: "TitanEdge", type: "dashboards", tags: ["titanedge"], asDropdown: true }],
    panels: layout(panels),
  };
}

const q = {
  rps: `sum(rate(titan_http_requests_total{${NS}}[1m]))`,
  err: `sum(rate(titan_http_requests_total{${NS},code=~"5.."}[5m])) / clamp_min(sum(rate(titan_http_requests_total{${NS}}[5m])), 1e-9)`,
  p: (qq) => `histogram_quantile(${qq}, sum by (le) (rate(titan_http_request_duration_seconds_bucket{${NS}}[1m])))`,
};

const dashboards = {
  "titanedge-overview.json": dashboard("titanedge-overview", "TitanEdge / Overview", [
    stat("Requests / s", q.rps, { unit: "reqps", decimals: 0 }),
    stat("5xx ratio", q.err, { unit: "percentunit", decimals: 3, thresholds: [{ color: "green", value: null }, { color: "orange", value: 0.001 }, { color: "red", value: 0.01 }] }),
    stat("p99 latency", q.p(0.99), { unit: "s", thresholds: [{ color: "green", value: null }, { color: "orange", value: 0.25 }, { color: "red", value: 0.5 }] }),
    stat("API pods", `count(up{${NS},job=~".*titan-api.*"} == 1)`),
    stat("Worker pods", `count(up{${NS},job=~".*titan-worker.*"} == 1)`),
    stat("Events / s", `sum(rate(titan_worker_events_total{${NS},result="ok"}[1m]))`, { unit: "short", decimals: 0 }),
    row("Traffic"),
    ts("Requests / s by route", [[`sum by (route) (rate(titan_http_requests_total{${NS}}[1m]))`, "{{route}}"]], { unit: "reqps", stack: true }),
    ts("Latency", [[q.p(0.5), "p50"], [q.p(0.95), "p95"], [q.p(0.99), "p99"], [q.p(0.999), "p99.9"]], { unit: "s" }),
    ts("Responses by status code", [[`sum by (code) (rate(titan_http_requests_total{${NS}}[1m]))`, "{{code}}"]], { unit: "reqps", stack: true }),
    ts("In-flight & load shedding", [[`sum(titan_http_inflight_requests{${NS}})`, "in-flight"], [`sum(rate(titan_http_shed_total{${NS}}[1m]))`, "shed / s"]]),
    row("Dependencies"),
    ts("Redis cache", [[`sum by (result) (rate(titan_cache_requests_total{${NS}}[1m]))`, "{{result}}"]], { unit: "ops", w: 8 }),
    ts("PostgreSQL p99 by operation", [[`histogram_quantile(0.99, sum by (le, op) (rate(titan_db_query_duration_seconds_bucket{${NS}}[5m])))`, "{{op}}"]], { unit: "s", w: 8 }),
    ts("Kafka produce", [[`sum by (result) (rate(titan_kafka_published_total{${NS}}[1m]))`, "{{result}}"]], { unit: "ops", w: 8 }),
    row("Scaling"),
    ts("Replicas", [[`sum by (deployment) (kube_deployment_status_replicas_available{${NS},deployment=~".*titan.*"})`, "{{deployment}}"]], { w: 12 }),
    ts("CPU by pod", [[`sum by (pod) (rate(container_cpu_usage_seconds_total{${NS},pod=~".*titan.*",container!=""}[1m]))`, "{{pod}}"]], { unit: "short", w: 12 }),
    ts("Memory by pod", [[`sum by (pod) (container_memory_working_set_bytes{${NS},pod=~".*titan.*",container!=""})`, "{{pod}}"]], { unit: "bytes", w: 12 }),
    ts("Go goroutines", [[`sum by (pod) (go_goroutines{${NS},job=~".*titan-(api|worker).*"})`, "{{pod}}"]], { w: 12 }),
  ], { vars: [nsVar("titan_http_requests_total")], tags: ["api"] }),

  "titanedge-edge.json": dashboard("titanedge-edge", "TitanEdge / Edge (NGINX + Envoy)", [
    stat("NGINX req/s", `sum(rate(nginx_http_requests_total{${NS}}[1m]))`, { unit: "reqps", w: 6 }),
    stat("NGINX active conns", `sum(nginx_connections_active{${NS}})`, { w: 6 }),
    stat("Envoy downstream req/s", `sum(rate(envoy_http_downstream_rq_total{${NS},envoy_http_conn_manager_prefix="ingress"}[1m]))`, { unit: "reqps", w: 6 }),
    stat("Rate limited / s", `sum(rate(envoy_edge_rate_limit_http_local_rate_limit_rate_limited{${NS}}[1m])) or vector(0)`, { unit: "reqps", w: 6 }),
    row("NGINX"),
    ts("NGINX requests / s", [[`sum by (pod) (rate(nginx_http_requests_total{${NS}}[1m]))`, "{{pod}}"]], { unit: "reqps" }),
    ts("NGINX connections", [[`sum by (state) (label_replace(nginx_connections_active{${NS}}, "state", "active", "", ""))`, "active"], [`sum(nginx_connections_waiting{${NS}})`, "waiting"], [`sum(nginx_connections_writing{${NS}})`, "writing"]]),
    row("Envoy"),
    ts("Upstream latency (titan_api)", [
      [`histogram_quantile(0.5, sum by (le) (rate(envoy_cluster_upstream_rq_time_bucket{${NS},envoy_cluster_name="titan_api"}[1m])))`, "p50"],
      [`histogram_quantile(0.99, sum by (le) (rate(envoy_cluster_upstream_rq_time_bucket{${NS},envoy_cluster_name="titan_api"}[1m])))`, "p99"],
    ], { unit: "ms" }),
    ts("Upstream responses by class", [[`sum by (envoy_response_code_class) (rate(envoy_cluster_upstream_rq_xx{${NS},envoy_cluster_name="titan_api"}[1m]))`, "{{envoy_response_code_class}}xx"]], { unit: "reqps", stack: true }),
    ts("Healthy API endpoints", [[`sum(envoy_cluster_membership_healthy{${NS},envoy_cluster_name="titan_api"})`, "healthy"], [`sum(envoy_cluster_membership_total{${NS},envoy_cluster_name="titan_api"})`, "total"]], { w: 8 }),
    ts("Retries & ejections", [[`sum(rate(envoy_cluster_upstream_rq_retry{${NS},envoy_cluster_name="titan_api"}[1m]))`, "retries"], [`sum(envoy_cluster_outlier_detection_ejections_active{${NS},envoy_cluster_name="titan_api"})`, "ejected hosts"]], { w: 8 }),
    ts("Circuit breakers open", [[`max(envoy_cluster_circuit_breakers_default_rq_open{${NS},envoy_cluster_name="titan_api"})`, "requests"], [`max(envoy_cluster_circuit_breakers_default_cx_open{${NS},envoy_cluster_name="titan_api"})`, "connections"]], { w: 8 }),
  ], { vars: [nsVar("envoy_cluster_upstream_rq_total")], tags: ["edge"] }),

  "titanedge-pipeline.json": dashboard("titanedge-pipeline", "TitanEdge / Event Pipeline (Kafka + KEDA)", [
    stat("Produced / s", `sum(rate(titan_kafka_published_total{${NS},result="ok"}[1m]))`, { unit: "ops", w: 6, decimals: 0 }),
    stat("Consumed / s", `sum(rate(titan_worker_events_total{${NS},result="ok"}[1m]))`, { unit: "ops", w: 6, decimals: 0 }),
    stat("Consumer lag (KEDA)", `max(keda_scaler_metrics_value{scaledObject=~".*worker.*"}) or vector(0)`, { w: 6, thresholds: [{ color: "green", value: null }, { color: "orange", value: 10000 }, { color: "red", value: 100000 }] }),
    stat("Workers", `max(kube_deployment_status_replicas_available{${NS},deployment=~".*titan-worker.*"})`, { w: 6 }),
    ts("Produce vs consume", [[`sum(rate(titan_kafka_published_total{${NS},result="ok"}[1m]))`, "produced"], [`sum(rate(titan_worker_events_total{${NS},result="ok"}[1m]))`, "consumed"]], { unit: "ops" }),
    ts("Lag vs workers", [[`max(keda_scaler_metrics_value{scaledObject=~".*worker.*"})`, "lag (per KEDA)"], [`max(kube_deployment_status_replicas_available{${NS},deployment=~".*titan-worker.*"})`, "workers"]]),
    ts("Producer errors & backpressure", [[`sum by (result) (rate(titan_kafka_published_total{${NS},result!="ok"}[1m]))`, "{{result}}"]], { unit: "ops", w: 8 }),
    ts("Flush latency", [[`histogram_quantile(0.99, sum by (le) (rate(titan_worker_flush_duration_seconds_bucket{${NS}}[5m])))`, "p99"], [`histogram_quantile(0.5, sum by (le) (rate(titan_worker_flush_duration_seconds_bucket{${NS}}[5m])))`, "p50"]], { unit: "s", w: 8 }),
    ts("Batch size", [[`histogram_quantile(0.5, sum by (le) (rate(titan_worker_batch_events_bucket{${NS}}[5m])))`, "median events / flush"]], { w: 8 }),
  ], { vars: [nsVar("titan_worker_events_total")], tags: ["kafka", "keda"] }),

  "titanload.json": dashboard("titanload", "TitanEdge / TitanLoad", [
    stat("Generated RPS", `sum(titanload_rps)`, { unit: "reqps", w: 6, decimals: 0 }),
    stat("p99 (client)", `max(titanload_latency_seconds{quantile="0.99"})`, { unit: "s", w: 6 }),
    stat("Error ratio", `max(titanload_error_ratio)`, { unit: "percentunit", w: 6, decimals: 3, thresholds: [{ color: "green", value: null }, { color: "red", value: 0.01 }] }),
    stat("Workers", `max(titanload_workers)`, { w: 6 }),
    ts("Client RPS vs server RPS", [[`sum(titanload_rps)`, "titanload (client)"], [`sum(rate(titan_http_requests_total[1m]))`, "titan-api (server)"]], { unit: "reqps", w: 24 }),
    ts("Client latency quantiles", [[`max by (quantile) (titanload_latency_seconds)`, "p{{quantile}}"]], { unit: "s" }),
    ts("Throughput", [[`sum(titanload_bytes_per_second)`, "bytes/s"]], { unit: "Bps" }),
    ts("Requests & failures", [[`sum(titanload_requests)`, "requests"], [`sum(titanload_failures)`, "failures"]]),
    ts("API replicas during test", [[`sum(kube_deployment_status_replicas_available{deployment=~".*titan-api.*"})`, "api pods"]]),
  ], { tags: ["load"], refresh: "5s", from: "now-15m" }),

  "titanedge-logs.json": dashboard("titanedge-logs", "TitanEdge / Logs", [
    {
      type: "timeseries",
      title: "Log lines / s by level",
      datasource: LOKI,
      gridPos: { w: 24, h: 7 },
      fieldConfig: { defaults: { unit: "short", custom: { stacking: { mode: "normal" }, fillOpacity: 30 } }, overrides: [] },
      options: { legend: { displayMode: "list", placement: "bottom" } },
      targets: [{ datasource: LOKI, refId: "A", expr: `sum by (level) (rate({namespace=~"$namespace"} | json | __error__="" [1m]))`, legendFormat: "{{level}}" }],
    },
    logs("Errors & warnings", `{namespace=~"$namespace"} | json | level=~"ERROR|WARN" |~ "$search"`, { h: 10 }),
    logs("All logs", `{namespace=~"$namespace"} |~ "$search"`, { h: 14 }),
  ], {
    vars: [
      { name: "namespace", type: "custom", query: "titanedge", current: { text: "titanedge", value: "titanedge" }, options: [] },
      { name: "search", label: "Search", type: "textbox", query: "", current: { text: "", value: "" } },
    ],
    tags: ["logs"],
  }),
};

mkdirSync(OUT, { recursive: true });
for (const [file, d] of Object.entries(dashboards)) {
  writeFileSync(join(OUT, file), JSON.stringify(d, null, 2) + "\n");
  console.log("wrote", join("charts/platform/dashboards", file));
}
