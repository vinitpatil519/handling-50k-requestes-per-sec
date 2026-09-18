import Dashboard from "@/components/Dashboard";
import type { Links } from "@/lib/api";

// Read tool links at request time so one image works in every environment.
export const dynamic = "force-dynamic";

export default function Page() {
  const links: Links = {
    grafana: process.env.GRAFANA_URL,
    jaeger: process.env.JAEGER_URL,
    argocd: process.env.ARGOCD_URL,
    prometheus: process.env.PROMETHEUS_URL,
  };
  return <Dashboard links={links} />;
}
