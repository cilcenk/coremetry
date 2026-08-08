import type { PipelineRule, PipelineCondition, PipelineCondOp } from '@/lib/api';

// pipelineRuleBody (v0.9.803) — PipelineRuleModal'ın form state'i → POST
// gövdesi. SAF ve TABLO-TESTLİ, çünkü burada kaybedilen bir alan sessiz VERİ
// KAYBI demek.
//
// Neden ayrı bir modül: modal gövdeyi satır içinde, sıfırdan kuruyordu —
// `{ id, name, kind, signal, enabled, when }` + kind'a göre setAttributes /
// rate. Kuralın taşıdığı ama formun göstermediği HER alan o anda düşüyordu ve
// v0.9.797'nin eklediği `and` tam olarak öyle bir alandı:
//
//   metric-excl-*  when: http.route =~ ^/health   AND   metric = http.server.duration
//
// Operatör bu kuralı listede görüp adını düzeltmek için Edit'e bassa, kayıt
// `and`'i düşürüyor ve kural "ŞU metriğin şu route'u" olmaktan çıkıp "HER
// metriğin şu route'u" hâline geliyordu — istenmeyen, geri alınamaz ingest
// drop'u (yazılmayan datapoint geri gelmez).
//
// Kural: form ALANLARI forma ait; formun bilmediği her şey gövdeye AYNEN
// taşınır. Modal `and`'i READ-ONLY gösterir (düzenleme yüzeyi açmadan
// görünürlük), buildPipelineRuleBody onu bire bir geri yazar.
export interface PipelineRuleForm {
  name: string;
  kind: PipelineRule['kind'];
  signal: PipelineRule['signal'];
  enabled: boolean;
  whenKey: string;
  whenOp: PipelineCondOp;
  whenValue: string;
  enrichKey: string;
  enrichValue: string;
  rate: number;
}

// carryConditions — `and` listesini normalize edip AYNEN taşır. Boş liste ile
// tanımsız arasındaki fark motorda anlamlı değil (matchAll boş listede true
// döner) ama gövdeye `and: []` basmak, kuralı JSON'da gereksiz yere
// farklılaştırır; yoksa alan da yok.
function carryConditions(and: PipelineCondition[] | undefined): PipelineCondition[] | undefined {
  if (!and || and.length === 0) return undefined;
  return and.map(c => ({ key: c.key, op: c.op, value: c.value }));
}

export function buildPipelineRuleBody(
  existing: PipelineRule | null,
  f: PipelineRuleForm,
): PipelineRule {
  const body: PipelineRule = {
    id: existing?.id ?? '',
    name: f.name.trim(),
    kind: f.kind,
    signal: f.signal,
    enabled: f.enabled,
    when: { key: f.whenKey.trim(), op: f.whenOp, value: f.whenValue.trim() },
  };
  // TAŞINAN alan — formun düzenlemediği ama kuralın sahip olduğu koşullar.
  const and = carryConditions(existing?.and);
  if (and) body.and = and;

  if (f.kind === 'enrich') {
    body.setAttributes = f.enrichKey.trim()
      ? { [f.enrichKey.trim()]: f.enrichValue.trim() }
      : {};
  }
  if (f.kind === 'sample') {
    body.rate = f.rate;
  }
  return body;
}

// describeCondition — READ-ONLY `and` rozetlerinin metni.
export function describeCondition(c: PipelineCondition): string {
  return `${c.key} ${c.op} ${c.value}`;
}
