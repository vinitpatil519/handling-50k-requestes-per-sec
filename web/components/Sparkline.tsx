export default function Sparkline({ values, height = 80 }: { values: number[]; height?: number }) {
  const width = 600;
  if (values.length < 2) {
    return <div className="spark-empty" style={{ height }}>collecting samples…</div>;
  }
  const max = Math.max(...values, 1);
  const step = width / (values.length - 1);
  const points = values.map((v, i) => `${(i * step).toFixed(1)},${(height - (v / max) * (height - 6) - 3).toFixed(1)}`);
  const area = `0,${height} ${points.join(" ")} ${width},${height}`;
  return (
    <svg viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" className="spark" style={{ height }}>
      <polygon points={area} className="spark-area" />
      <polyline points={points.join(" ")} className="spark-line" />
    </svg>
  );
}
