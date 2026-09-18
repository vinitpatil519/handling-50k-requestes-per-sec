"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api, formatNumber, formatUptime, type Item, type Links, type Stats } from "@/lib/api";
import Sparkline from "./Sparkline";

type PodView = Stats & { seenAt: number; rps: number };

const POLL_MS = 1500;
const HISTORY = 80;

export default function Dashboard({ links }: { links: Links }) {
  const [latest, setLatest] = useState<Stats | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [pods, setPods] = useState<Record<string, PodView>>({});
  const [eventRate, setEventRate] = useState<number[]>([]);
  const [items, setItems] = useState<Item[]>([]);
  const [name, setName] = useState("");
  const [burst, setBurst] = useState(100);
  const [burstResult, setBurstResult] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const lastEvents = useRef<{ n: number; t: number } | null>(null);

  const poll = useCallback(async () => {
    try {
      const s = await api<Stats>("/stats");
      const now = Date.now();
      setLatest(s);
      setError(null);
      setPods((prev) => {
        const old = prev[s.pod];
        const dt = old ? (now - old.seenAt) / 1000 : 0;
        const rps = old && dt > 0 ? Math.max(0, (s.requests_local - old.requests_local) / dt) : 0;
        const next = { ...prev, [s.pod]: { ...s, seenAt: now, rps } };
        // Forget pods not seen for 30s (scaled down or restarted).
        for (const [k, v] of Object.entries(next)) if (now - v.seenAt > 30_000) delete next[k];
        return next;
      });
      if (s.events_processed !== undefined) {
        const prev = lastEvents.current;
        if (prev && now > prev.t) {
          const rate = Math.max(0, (s.events_processed - prev.n) / ((now - prev.t) / 1000));
          setEventRate((h) => [...h, rate].slice(-HISTORY));
        }
        lastEvents.current = { n: s.events_processed, t: now };
      }
    } catch (e) {
      setError((e as Error).message);
    }
  }, []);

  const loadItems = useCallback(async () => {
    try {
      const res = await api<{ items: Item[] }>("/items?limit=10");
      setItems(res.items ?? []);
    } catch {
      setItems([]);
    }
  }, []);

  useEffect(() => {
    poll();
    loadItems();
    const t = setInterval(poll, POLL_MS);
    return () => clearInterval(t);
  }, [poll, loadItems]);

  async function createItem(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;
    setBusy(true);
    try {
      await api<Item>("/items", {
        method: "POST",
        body: JSON.stringify({ name: name.trim(), payload: { source: "dashboard" } }),
      });
      setName("");
      await loadItems();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function fireEvents() {
    setBusy(true);
    setBurstResult(null);
    const started = performance.now();
    let ok = 0;
    let failed = 0;
    const batch = 50;
    for (let i = 0; i < burst; i += batch) {
      const n = Math.min(batch, burst - i);
      const results = await Promise.allSettled(
        Array.from({ length: n }, (_, j) =>
          api("/events", {
            method: "POST",
            body: JSON.stringify({ type: "dashboard.click", data: { seq: i + j } }),
          }),
        ),
      );
      for (const r of results) r.status === "fulfilled" ? ok++ : failed++;
    }
    const ms = performance.now() - started;
    setBurstResult(`${ok} accepted, ${failed} failed in ${ms.toFixed(0)} ms (${formatNumber((ok / ms) * 1000)} req/s from this browser)`);
    setBusy(false);
  }

  const podList = Object.values(pods).sort((a, b) => a.pod.localeCompare(b.pod));
  const deps = latest?.dependencies;

  return (
    <main className="shell">
      <header className="top">
        <div>
          <h1>
            <span className="logo">▲</span> TitanEdge <span className="muted">Control Plane</span>
          </h1>
          <p className="muted">NGINX → Envoy → Go API → Redis · PostgreSQL · Kafka</p>
        </div>
        <nav className="links">
          {links.grafana && <a href={links.grafana} target="_blank" rel="noreferrer">Grafana</a>}
          {links.prometheus && <a href={links.prometheus} target="_blank" rel="noreferrer">Prometheus</a>}
          {links.jaeger && <a href={links.jaeger} target="_blank" rel="noreferrer">Jaeger</a>}
          {links.argocd && <a href={links.argocd} target="_blank" rel="noreferrer">Argo CD</a>}
        </nav>
      </header>

      {error && <div className="banner error">API error: {error}</div>}

      <section className="grid">
        <Card label="Serving pod" value={latest?.pod ?? "—"} hint={latest ? `v${latest.version}` : undefined} />
        <Card label="Uptime" value={latest ? formatUptime(latest.uptime_seconds) : "—"} />
        <Card label="Items" value={formatNumber(latest?.items)} hint="PostgreSQL (estimate)" />
        <Card label="Events processed" value={formatNumber(latest?.events_processed)} hint="Kafka → worker → Redis" />
        <Card
          label="In-flight"
          value={latest ? `${formatNumber(latest.inflight)} / ${formatNumber(latest.max_inflight)}` : "—"}
          hint="admission control"
        />
        <Card label="Goroutines" value={formatNumber(latest?.goroutines)} />
      </section>

      <section className="row">
        <div className="panel grow">
          <h2>Event pipeline throughput</h2>
          <p className="muted">events/s persisted by the worker fleet (KEDA scales on consumer lag)</p>
          <Sparkline values={eventRate} height={90} />
          <div className="inline">
            <strong>{formatNumber(eventRate.at(-1) ?? 0)}</strong>
            <span className="muted">events/s now</span>
          </div>
        </div>
        <div className="panel">
          <h2>Dependencies</h2>
          <ul className="deps">
            <Dep name="PostgreSQL" on={deps?.postgres} />
            <Dep name="Redis" on={deps?.redis} />
            <Dep name="Kafka" on={deps?.kafka} />
          </ul>
        </div>
      </section>

      <section className="panel">
        <h2>API pods observed</h2>
        <p className="muted">Each poll lands on whichever pod Envoy picks — watch requests spread as the HPA scales out.</p>
        <table>
          <thead>
            <tr>
              <th>Pod</th>
              <th>Requests served</th>
              <th>Observed rps</th>
              <th>In-flight</th>
              <th>Uptime</th>
            </tr>
          </thead>
          <tbody>
            {podList.length === 0 && (
              <tr>
                <td colSpan={5} className="muted">waiting for data…</td>
              </tr>
            )}
            {podList.map((p) => (
              <tr key={p.pod}>
                <td className="mono">{p.pod}</td>
                <td>{formatNumber(p.requests_local)}</td>
                <td>{formatNumber(p.rps)}</td>
                <td>{formatNumber(p.inflight)}</td>
                <td>{formatUptime(p.uptime_seconds)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>

      <section className="row">
        <div className="panel grow">
          <h2>Items</h2>
          <form onSubmit={createItem} className="inline">
            <input value={name} onChange={(e) => setName(e.target.value)} placeholder="new item name" maxLength={200} />
            <button disabled={busy || !name.trim()}>Create</button>
            <button type="button" className="ghost" onClick={loadItems}>Refresh</button>
          </form>
          <table>
            <thead>
              <tr>
                <th>ID</th>
                <th>Name</th>
                <th>Created</th>
              </tr>
            </thead>
            <tbody>
              {items.map((it) => (
                <tr key={it.id}>
                  <td className="mono">{it.id}</td>
                  <td>{it.name}</td>
                  <td className="muted">{new Date(it.created_at).toLocaleString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <div className="panel">
          <h2>Fire events</h2>
          <p className="muted">POST /api/v1/events from this browser.</p>
          <div className="inline">
            <select value={burst} onChange={(e) => setBurst(Number(e.target.value))}>
              {[10, 100, 1000, 5000].map((n) => (
                <option key={n} value={n}>{formatNumber(n)} events</option>
              ))}
            </select>
            <button onClick={fireEvents} disabled={busy}>Send</button>
          </div>
          {burstResult && <p className="result">{burstResult}</p>}
          <p className="muted small">
            For real load use <code>titanload</code>:<br />
            <code>titanload run -u http://localhost:8080/api/v1/ping -c 256 -d 60s</code>
          </p>
        </div>
      </section>
    </main>
  );
}

function Card({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="card">
      <div className="label">{label}</div>
      <div className="value">{value}</div>
      {hint && <div className="hint">{hint}</div>}
    </div>
  );
}

function Dep({ name, on }: { name: string; on?: boolean }) {
  const state = on === undefined ? "unknown" : on ? "up" : "off";
  return (
    <li>
      <span className={`dot ${state}`} /> {name}
      <span className="muted"> {state === "up" ? "connected" : state === "off" ? "disabled" : "…"}</span>
    </li>
  );
}
