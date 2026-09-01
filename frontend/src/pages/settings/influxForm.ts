// influxForm (v0.10.222) — InfluxTab'ın METİN kutuları ile tel arasındaki
// çeviri; vmForm.ts deseni: SAF ve TABLO-TESTLİ.
//
// İki yönlü sözleşmeler:
//   • attrMap: satır başına `TAG=attr` (ya da `TAG: attr`); boş satır ve
//     `#` yorumu atlanır; tel'e Record, boşsa undefined (omitempty).
//   • groupBy: virgül/yeni satırla ayrılmış tag adları; boşsa [].
//   • eşikler: '' = unset → tel'e YAZILMAZ (0/omitempty = global varsayılan).
//     vmForm dersi: kutuya varsayılanı basmak sessiz ayar donması demek.
//
// TFAIL şablonu spec'ten (docs/audit/influx-integration.md §0 K3/K4):
// SORGU 1 gauge (son 2 dk), SORGU 2 enrichment; groupBy v1'de yalnız
// OPERATIONCODE+ERRORCODE, attrMap altı tag'in tümünü adlandırır.
import type { InfluxQueryConfig, InfluxThresholds } from '@/lib/types';

export function parseAttrMap(text: string): Record<string, string> | undefined {
  const out: Record<string, string> = {};
  for (const raw of text.split('\n')) {
    const line = raw.trim();
    if (!line || line.startsWith('#')) continue;
    const m = /^([^=:\s]+)\s*[=:]\s*(.+)$/.exec(line);
    if (!m) continue;
    out[m[1].trim()] = m[2].trim();
  }
  return Object.keys(out).length ? out : undefined;
}

export function attrMapToText(m: Record<string, string> | undefined | null): string {
  if (!m) return '';
  return Object.entries(m).map(([k, v]) => `${k}=${v}`).join('\n');
}

export function parseList(text: string): string[] {
  return text.split(/[,\n]/).map(s => s.trim()).filter(Boolean);
}

export function listToText(l: string[] | undefined | null): string {
  return (l ?? []).join(', ');
}

/** '' | garbage → undefined (unset); sayı → sayı. Negatifi sunucu reddeder. */
export function numFromForm(text: string): number | undefined {
  const t = text.trim();
  if (t === '') return undefined;
  const n = Number(t);
  return Number.isFinite(n) ? n : undefined;
}

export function numToForm(v: number | undefined | null): string {
  return v === undefined || v === null || v === 0 ? '' : String(v);
}

export interface ThresholdsForm { criticalZ: string; dwell: string; minAbsDelta: string; minMAD: string }

export function thresholdsToForm(t: InfluxThresholds | undefined): ThresholdsForm {
  return {
    criticalZ: numToForm(t?.criticalZ), dwell: numToForm(t?.dwell),
    minAbsDelta: numToForm(t?.minAbsDelta), minMAD: numToForm(t?.minMAD),
  };
}

/** Hiçbir kutu doluysa undefined — tel'e boş nesne bile gitmez. */
export function thresholdsToWire(f: ThresholdsForm): InfluxThresholds | undefined {
  const out: InfluxThresholds = {};
  const cz = numFromForm(f.criticalZ); if (cz !== undefined) out.criticalZ = cz;
  const dw = numFromForm(f.dwell); if (dw !== undefined) out.dwell = dw;
  const ad = numFromForm(f.minAbsDelta); if (ad !== undefined) out.minAbsDelta = ad;
  const mm = numFromForm(f.minMAD); if (mm !== undefined) out.minMAD = mm;
  return Object.keys(out).length ? out : undefined;
}

export const TFAIL_TEMPLATE: InfluxQueryConfig = {
  name: 'tfail_adet',
  flux: `from(bucket: "GGFailTraceBckt")
  |> range(start: -2m)
  |> filter(fn: (r) => r._measurement == "TFAIL" and r._field == "ADET")
  |> group(columns: ["OPERATIONCODE", "ERRORCODE"])
  |> sum()`,
  enrichFlux: `from(bucket: "GGFailTraceBckt")
  |> range(start: {{from}}, stop: {{to}})
  |> filter(fn: (r) => r._measurement == "TFAIL" and r._field == "ADET")
  |> filter(fn: (r) => r.OPERATIONCODE == "{{op}}" and r.ERRORCODE == "{{err}}")
  |> keep(columns: ["_time", "TRACEID", "INSTANCEID", "FUNCTIONCODE", "KANALKOD"])
  |> group()
  |> sort(columns: ["_time"], desc: true)
  |> limit(n: 50)`,
  groupBy: ['OPERATIONCODE', 'ERRORCODE'],
  attrMap: {
    OPERATIONCODE: 'operation',
    FUNCTIONCODE: 'FUNCTION_CODE',
    KANALKOD: 'CHANNEL_CODE',
    INSTANCEID: 'k8s.pod.name',
    TRACEID: 'trace_id',
    ERRORCODE: 'error.code',
  },
};
