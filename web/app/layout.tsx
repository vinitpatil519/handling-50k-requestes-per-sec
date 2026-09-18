import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "TitanEdge Control Plane",
  description: "Live view of the TitanEdge hyperscale platform",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
