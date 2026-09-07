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
// REDACTED şablonu — v0.10.224: operatörün GERÇEK Grafana sorgusuyla hizalı;
// v0.10.526'da GoldenGate ekibinin sorgusuyla yenilendi (aşağıdaki not)
// ("BAŞARISIZ FONKSİYON VE OPERASYONLAR", 2026-09-01). Bucket spec'teki
REDACTED
// okuyor — farklıysa kutudan değiştir. Gürültü filtreleri (REDACTED != "0", REDACTED
// != "N/A", "------" dışlaması), `aggregateWindow(every: 1m, fn: sum,
// createEmpty: false)` → dakikalık toplam; Coremetry'deki dakika Grafana'daki
// dakikayla aynı sayı. Grafana'nın `_value > 4` ekran tabanı BİLEREK YOK:
// dedektör baseline için düşük değerleri de görmeli, gürültü tabanı
// eşiklerdeki MinAbsDelta'nın işi. groupBy v1'de REDACTED+REDACTED
// (spec); Grafana REDACTED+REDACTED+REDACTED gruplar — kutudan
// değiştirilebilir (kardinalite notu sekmede).
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

// v0.10.526 — operatör (2026-09-07): GoldenGate ekibinin verdiği iki Grafana
// sorgusu VARSAYILAN şablon oldu ("default bunlar olsun, Coremetry Influx
// entegrasyonuna uygun"). Grafana kalıbından üç uyarlama: `v.timeRangeStart`
// → göreli `range(start: -2m)` (poller watermark'la kovayı bir kez yazar),
// `v.windowPeriod` → sabit `every: 1m` (Coremetry'deki dakika = Grafana'daki
// dakika), `createEmpty: true` → `false` (null _value "kötü değer" diye
// düşer; sıfır dolgusu D3 dedektöründe). Ekibin `group(columns:
// ["_measurement"])`i tek seri verirdi; operatör kanal + operasyon istedi →
// REDACTED + REDACTED (sekmeden değiştirilebilir, 5.000 seri tavanı).
// v0.10.528 (operatör: "kanal kodu başka olabilir, 010101 şart değil; sorguda
// filtre kalmış") — ekibin örneğindeki REDACTED =~ /^01/ süzgeci KALDIRILDI:
// tüm kanallar; kanal boyutu groupBy'da zaten var. Kardinalite: kanal ×
// operasyon 5.000 seri tavanını aşarsa gruplamadan REDACTED'u çıkar.
// v0.10.527 — poll penceresi 2 dk → 2 sa: prod "Bağlantıyı dene" (2026-09-07)
// poll penceresinde 0 satır, 24 sa'te 8.414 satır ve en yeni _time probe
// anından 50 dk geride gösterdi — GoldenGate kaynağı GECİKMELİ yazıyor.
// Watermark aynı kovayı bir kez yazdığından geniş pencere güvenli; 2 sa
// (6 satır/dk hacimde) ucuz. Kaynağın gecikmesi 2 sa'yi aşarsa sekmeden
// büyüt.
export const REDACTEDTEMPLATE: InfluxQueryConfig = {
  name: 'REDACTED',
  REDACTED")
  |> range(start: -2h)
  |> filter(fn: (r) => r._measurement == "REDACTED" and r._field == "REDACTED")
  |> group(columns: ["REDACTED", "REDACTED"])
  |> aggregateWindow(every: 1m, fn: sum, createEmpty: false)
  |> yield(name: "sum")`,
  // SORGU 2 — kanıt: problem açılınca aynı grubun son 50 TRACEID'si.
  // Yer tutucular groupBy tag adlarıyla (enrich.go: her groupBy tag'ı
  // adıyla doldurulur) + {{from}}/{{to}}.
  REDACTED")
  |> range(start: {{from}}, stop: {{to}})
  |> filter(fn: (r) => r._measurement == "REDACTED" and r._field == "REDACTED")
  |> filter(fn: (r) => r.REDACTED == "{{REDACTED}}" and r.REDACTED == "{{REDACTED}}")
  |> keep(columns: ["_time", "TRACEID", "REDACTED", "REDACTED", "REDACTED"])
  |> group()
  |> sort(columns: ["_time"], desc: true)
  |> limit(n: 50)`,
  groupBy: ['REDACTED', 'REDACTED'],
  attrMap: {
    REDACTED: 'operation',
    REDACTED: 'FUNCTION_CODE',
    REDACTED: 'CHANNEL_CODE',
    REDACTED: 'k8s.pod.name',
    TRACEID: 'trace_id',
    REDACTED: 'error.code',
  },
};

REDACTED
// REDACTED toplamı (başarılı + başarısız), aynı gruplama (kanal süzgeci yok, v0.10.528).
// Hata oranı (REDACTED ÷ toplam) bugün türetilmiyor; iki seri ayrı izlenir,
// Explore'da yan yana çizilir. Kanıt sorgusu YOK (TRACEID bu bucket'ta
// spec'te yok).
export const GG_TOTAL_TEMPLATE: InfluxQueryConfig = {
  name: 'gg_adet_total',
  REDACTED")
  |> range(start: -2h)
  |> filter(fn: (r) => r._field == "REDACTED")
  |> group(columns: ["REDACTED", "REDACTED"])
  |> aggregateWindow(every: 1m, fn: sum, createEmpty: false)
  |> yield(name: "sum")`,
  groupBy: ['REDACTED', 'REDACTED'],
  attrMap: {
    REDACTED: 'operation',
    REDACTED: 'FUNCTION_CODE',
    REDACTED: 'CHANNEL_CODE',
  },
};

/** v0.10.231 (D6) — groupBy metnine bir tag ekler/çıkarır (REDACTED anahtarı).
 *  Sıra korunur; ekleme sona; büyük/küçük harf duyarlı (Influx tag'leri öyle). */
export function toggleGroupByTag(text: string, tag: string, on: boolean): string {
  const list = parseList(text).filter(t => t !== tag);
  if (on) list.push(tag);
  return listToText(list);
}

export function hasGroupByTag(text: string, tag: string): boolean {
  return parseList(text).includes(tag);
}
