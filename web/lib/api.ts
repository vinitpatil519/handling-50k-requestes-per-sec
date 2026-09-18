// Types shared by the dashboard and the API proxy.

export type Stats = {
  pod: string;
  version: string;
  uptime_seconds: number;
  goroutines: number;
  requests_local: number;
  inflight: number;
  max_inflight: number;
  events_processed?: number;
  items?: number;
  dependencies: { postgres: boolean; redis: boolean; kafka: boolean };
};

export type Item = {
  id: number;
  name: string;
  payload: unknown;
  created_at: string;
};

export type Links = {
  grafana?: string;
  jaeger?: string;
  argocd?: string;
  prometheus?: string;
};

// Browser code always calls same-origin /api/v1. In Kubernetes NGINX routes
// that path to Envoy; in `next dev` the route handler proxies to API_BASE_URL.
export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
    cache: "no-store",
  });
  const text = await res.text();
  const body = text ? JSON.parse(text) : {};
  if (!res.ok) {
    throw new Error(body.error ?? `HTTP ${res.status}`);
  }
  return body as T;
}

export function formatNumber(n: number | undefined): string {
  if (n === undefined || Number.isNaN(n)) return "—";
  return new Intl.NumberFormat("en-US").format(Math.round(n));
}

export function formatUptime(s: number): string {
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m ${Math.floor(s % 60)}s`;
}
