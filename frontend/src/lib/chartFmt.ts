// Chart-specific formatters and palette. Centralised so every
// chart on the app reads the same way — same number rendered
// the same, same series rendered the same colour. The polish
// gap between "amateur" and "Datadog/Grafana" is mostly here:
// raw numbers like "12500" and one-off colours via hashColor
// look home-rolled; "12.5k rps" with a curated 10-colour
// palette looks like a product.

// fmtSmart — turns a raw value into a human-readable label,
// unit-aware. Axis ticks + tooltip values + KPI tiles all
// share this so e.g. P99 latency reads "234ms" everywhere
// rather than "234.567" in one place and "234ms" in another.
export function fmtSmart(v: number | null | undefined, unit?: string): string {
  if (v == null || !isFinite(v)) return '—';
  const u = (unit || '').trim();

  // Time — auto-promote ms → s → m so a 5-minute chart axis
  // doesn't read "300000ms".
  if (u === 'ms') {
    const abs = Math.abs(v);
    if (abs >= 60_000) return trim(v / 60_000) + 'm';
    if (abs >= 1_000)  return trim(v / 1_000)  + 's';
    if (abs >= 10)     return v.toFixed(0)     + 'ms';
    if (abs >= 1)      return v.toFixed(1)     + 'ms';
    return v.toFixed(2) + 'ms';
  }

  if (u === 's') {
    const abs = Math.abs(v);
    if (abs >= 60) return trim(v / 60) + 'm';
    if (abs >= 1)  return trim(v) + 's';
    return (v * 1000).toFixed(0) + 'ms';
  }

  if (u === '%') {
    const abs = Math.abs(v);
    if (abs >= 100) return v.toFixed(0) + '%';
    if (abs >= 10)  return v.toFixed(1) + '%';
    return v.toFixed(2) + '%';
  }

  // Bytes — IEC-style (1k = 1000) so it matches CH's
  // formatReadableSize defaults the operator already sees in
  // the cardinality dashboard.
  if (u === 'B' || u === 'bytes') return fmtBytes(v);

  // Throughput-like — count + " unit"
  if (/^(rps|qps|eps|msg\/s|\/s|ops|ops\/s)$/i.test(u)) {
    return fmtCount(v) + ' ' + u;
  }

  // Default — count + optional unit
  return fmtCount(v) + (u ? ' ' + u : '');
}

// fmtCount — k / M / G / T suffix. Two-decimal precision under
// the next decade so the eye reads "1.23k" not "1230".
function fmtCount(v: number): string {
  const abs = Math.abs(v);
  if (abs >= 1e12) return trim(v / 1e12) + 'T';
  if (abs >= 1e9)  return trim(v / 1e9)  + 'G';
  if (abs >= 1e6)  return trim(v / 1e6)  + 'M';
  if (abs >= 1e3)  return trim(v / 1e3)  + 'k';
  if (abs >= 100)  return v.toFixed(0);
  if (abs >= 10)   return v.toFixed(1);
  if (abs >= 1)    return v.toFixed(2);
  if (abs > 0)     return v.toFixed(3);
  return '0';
}

function fmtBytes(v: number): string {
  const abs = Math.abs(v);
  if (abs >= 1e12) return trim(v / 1e12) + ' TB';
  if (abs >= 1e9)  return trim(v / 1e9)  + ' GB';
  if (abs >= 1e6)  return trim(v / 1e6)  + ' MB';
  if (abs >= 1e3)  return trim(v / 1e3)  + ' kB';
  return v.toFixed(0) + ' B';
}

// trim — drop trailing ".00" / ".50" → "0.5" so "1.00k"
// reads as "1k" but "1.23k" stays as is.
function trim(v: number): string {
  // Use 2 decimals if the value is < 10 in its own decade,
  // 1 decimal if 10-99, 0 if ≥100. Rounded toFixed first to
  // avoid float-noise like "1.2300000000000001".
  const abs = Math.abs(v);
  let s: string;
  if (abs >= 100)     s = v.toFixed(0);
  else if (abs >= 10) s = v.toFixed(1);
  else                s = v.toFixed(2);
  // Strip trailing zeros after decimal point: "1.20" → "1.2",
  // "1.00" → "1".
  return s.includes('.') ? s.replace(/\.?0+$/, '') : s;
}

// ── Series colour palette ───────────────────────────────────
//
// Curated 10-colour palette chosen for:
//   • Distinguishability — every pair has a clear hue gap.
//   • Light/dark parity — saturation tuned so nothing reads
//     fluorescent on white or muddy on dark.
//   • Stability — same series name lands on the same colour
//     across every chart in the app, so an operator who learns
//     "frontend = blue" carries that mental model around.
//
// Order is intentional — the first few are the most readable
// when only a couple of series are on screen (Datadog blue,
// orange, green is the "primary trio" most APM tools converge
// on).
const PALETTE = [
  '#388bfd', // blue        — Coremetry accent
  '#f0703f', // orange
  '#3fb950', // green
  '#a371f7', // purple
  '#f5b343', // amber
  '#39c5cf', // cyan
  '#db61a2', // pink
  '#6dbf5b', // light green
  '#d29922', // gold
  '#7d8590', // neutral grey
];

// seriesColor — FNV-1a hash on the series label, then mod by
// palette length. Deterministic so the same label always hits
// the same colour; uses a hash (rather than first-come-first-
// assigned) so two charts that share series-set order still
// get a consistent mapping.
export function seriesColor(label: string): string {
  let h = 2166136261;
  for (let i = 0; i < label.length; i++) {
    h ^= label.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return PALETTE[Math.abs(h) % PALETTE.length];
}

// fmtXTicks — shared time-axis label formatter for uPlot
// charts. v0.5.380 fix: take the SPLITS array (full list of
// tick timestamps uPlot picked) and choose a label format
// scaled to the window:
//   • single-day spans → HH:MM only (no calendar redundancy)
//   • multi-day spans  → MM-DD HH:MM
// Pre-fix used a fixed MM-DD HH:MM per label, which collided
// horizontally on narrow charts where uPlot expected shorter
// labels (operator-reported: "tarihler üst üste").
export function fmtXTicks(splits: number[]): string[] {
  if (splits.length === 0) return [];
  const first = splits[0];
  const last = splits[splits.length - 1];
  const sameDay = splits.length > 1
    ? new Date(first * 1000).toDateString() === new Date(last * 1000).toDateString()
    : true;
  return splits.map(s => {
    const d = new Date(s * 1000);
    const hh = String(d.getHours()).padStart(2, '0');
    const mi = String(d.getMinutes()).padStart(2, '0');
    if (sameDay) return `${hh}:${mi}`;
    const mm = String(d.getMonth() + 1).padStart(2, '0');
    const dd = String(d.getDate()).padStart(2, '0');
    return `${mm}-${dd} ${hh}:${mi}`;
  });
}

// niceTickValues — given a min / max range, pick "round"
// gridline values an operator's eye can read. uPlot picks
// reasonable defaults but for ms / % / bytes the auto-picker
// produces awkward fractions; we override with snap-to-decade.
export function niceTickValues(min: number, max: number, target = 6): number[] {
  if (!isFinite(min) || !isFinite(max) || max <= min) return [];
  const range = max - min;
  // Pick the largest "nice" step that fits target ticks.
  const rough = range / target;
  const mag = Math.pow(10, Math.floor(Math.log10(rough)));
  // Round to the nearest 1 / 2 / 5 of that magnitude.
  const norm = rough / mag;
  const step = (norm < 1.5 ? 1 : norm < 3 ? 2 : norm < 7 ? 5 : 10) * mag;
  const ticks: number[] = [];
  const start = Math.ceil(min / step) * step;
  for (let v = start; v <= max + step / 2; v += step) {
    ticks.push(v);
  }
  return ticks;
}
