// Development / fallback proxy: forwards /api/v1/* to the Go API.
// In Kubernetes, NGINX sends /api/v1 straight to Envoy and this never runs,
// but it keeps `npm run dev` and single-container setups working.
import type { NextRequest } from "next/server";

export const dynamic = "force-dynamic";

const API_BASE_URL = process.env.API_BASE_URL ?? "http://localhost:8080";

async function proxy(req: NextRequest, ctx: { params: Promise<{ path: string[] }> }) {
  const { path } = await ctx.params;
  const url = new URL(`/api/v1/${path.map(encodeURIComponent).join("/")}`, API_BASE_URL);
  url.search = req.nextUrl.search;

  const headers = new Headers();
  for (const h of ["content-type", "accept", "x-request-id", "traceparent", "tracestate"]) {
    const v = req.headers.get(h);
    if (v) headers.set(h, v);
  }

  try {
    const upstream = await fetch(url, {
      method: req.method,
      headers,
      body: req.method === "GET" || req.method === "HEAD" ? undefined : await req.text(),
      cache: "no-store",
      signal: AbortSignal.timeout(10_000),
    });
    const out = new Headers();
    for (const h of ["content-type", "x-request-id", "x-cache", "retry-after"]) {
      const v = upstream.headers.get(h);
      if (v) out.set(h, v);
    }
    return new Response(upstream.body, { status: upstream.status, headers: out });
  } catch (err) {
    return Response.json(
      { error: `upstream unavailable: ${(err as Error).message}` },
      { status: 502 },
    );
  }
}

export { proxy as GET, proxy as POST, proxy as PUT, proxy as DELETE };
