// ExternalLinksTab — v0.10.345: trace sayfası dış link şablonları (operatör:
// function_id + channel_code ile içerideki log izleme platformuna tek tıkla).
// Şablon değişkenleri: {{attr.KEY}} · {{attrTime.KEY:FMT}} · {{time:FMT}} · {{endTime:FMT}} ·
// {{traceId}} · {{service}} · {{requestId}} (v0.10.566, log gövdesindeki istek
// kimliği) (FMT: dd MM yyyy yy HH mm ss). Host adı burada yaşar, repoda değil.
// Önizleme örnek değerlerle saf render'ı gösterir.
//
// v0.10.566 — `group`: aynı gruptaki linklerden yalnız ÇÖZÜLEN İLK link trace
// sayfasında çizilir. Operatörün iki yolu (birincil request_id, yedek
// function_id + channel_code) tek düğmede toplanır.
import { useEffect, useState } from 'react';
import { Button } from '@/components/ui/Button';
import { Field } from '@/components/ui/Field';
import { Stack, Row } from '@/components/ui';
import { CopyButton } from '@/components/CopyButton';
import { api } from '@/lib/api';
import type { ExternalLink } from '@/lib/types';
import { renderExternalLink } from '@/lib/externalLinks';
import { FlashBox } from './shared';

// v0.10.372 — operatör: "şablona ihtiyacım yok, date'i trace'in olduğu dakika
// ilet" → örnek trace başlangıç dakikasını ({{time}}) taşır; kimlik içindeki
// zaman (attrTime) isteğin üretim anıdır ve trace'ten önce kalabilir.
const EXAMPLE = 'https://log-platformu.example/masterlog?date={{time:ddMMyyyyHHmm}}&functionId={{attr.function_id}}&channelCode={{attr.channel_code}}';
// v0.10.566 (operatör) — BİRİNCİL yol: trace'in loglarının gövdesinde
// request_id varsa link onunla üretilir ve channelCode GÖNDERİLMEZ. İkisi
// aynı GRUPTA olduğu için trace sayfasında tek düğme çizilir: request_id
// çözülürse birincil, çözülmezse function_id yedeği.
const EXAMPLE_REQUEST_ID = 'https://log-platformu.example/masterlog?date={{time:ddMMyyyyHHmm}}&requestId={{requestId}}';
const EXAMPLE_GROUP = 'log-platformu';
const PREVIEW_CTX = {
  traceId: '0fcd70a94ba1f695ea079750e71a7c10', service: 'ornek-servis', startMs: Date.now(), endMs: Date.now() + 1_000,
  attrs: { function_id: '060201abcd00136801642026090416144424810', channel_code: '060201' },
  // Önizleme ÖRNEK değerlerle çalışır; gerçek trace'te request_id log
  // gövdesinden gelir ve olmayabilir (o zaman gruptaki yedek çizilir).
  requestId: 'a1b2c3d4-0000-4f00-9c00-000000000042',
  // v0.10.567 — önizleme de kurumun dilimini gösterir (gerçekte reqid.timezone).
  tz: 'Europe/Istanbul',
};

export function ExternalLinksTab() {
  const [links, setLinks] = useState<ExternalLink[]>([]);
  const [draft, setDraft] = useState<{ label: string; urlTemplate: string; color: string; group: string }>({ label: '', urlTemplate: '', color: '', group: '' });
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);
  useEffect(() => {
    api.getExternalLinks().then(d => setLinks(d.links ?? [])).catch(e => setMsg({ kind: 'err', text: e instanceof Error ? e.message : 'Yüklenemedi' }));
  }, []);
  const save = async (next: ExternalLink[]) => {
    setBusy(true); setMsg(null);
    try {
      const d = await api.putExternalLinks(next);
      setLinks(d.links ?? []);
      setMsg({ kind: 'ok', text: 'Kaydedildi.' });
    } catch (e) {
      setMsg({ kind: 'err', text: e instanceof Error ? e.message : 'Kaydedilemedi' });
    } finally { setBusy(false); }
  };
  const preview = draft.urlTemplate ? renderExternalLink(draft.urlTemplate, PREVIEW_CTX) : null;
  return (
    <Stack gap={4}>
      <div className="field-hint">
        Trace sayfasında "Explain this trace" yanında düğme olarak görünür; şablondaki attribute'lar trace'in
        span'lerinde çözülürse etkin, çözülmezse eksikleri söyleyerek pasif. Değişkenler:{' '}
        <code>{'{{attr.KEY}}'}</code> · <code>{'{{attrTime.KEY:FMT}}'}</code> (değerin içindeki yyyyMMddHHmmss, yeniden biçimlenir) ·{' '}
        <code>{'{{time:FMT}}'}</code> (trace başlangıcı, tarayıcı saati — dakika pencereli log platformları için bunu kullanın; kimlik içindeki zaman isteğin üretim anıdır, trace'ten önce kalabilir) · <code>{'{{endTime:FMT}}'}</code> (trace bitişi) · <code>{'{{traceId}}'}</code> · <code>{'{{service}}'}</code> ·{' '}
        <code>{'{{requestId}}'}</code> (trace'in loglarının gövdesindeki istek kimliği; yoksa şablon çözülmez). FMT: dd MM yyyy yy HH mm ss.
      </div>
      {/* v0.10.566 — grup: aynı log platformuna giden iki şablonu (birincil
          requestId + yedek function_id) tek düğmede topla. */}
      <div className="field-hint">
        <b>Grup:</b> aynı gruptaki linklerden, buradaki sırayla, yalnız <b>çözülen ilk</b> link çizilir; boş = link tek başına.
        Hiçbiri çözülmezse grubun ilk linki pasif olarak eksiklerini söyler.
      </div>
      {msg && <FlashBox kind={msg.kind}>{msg.text}</FlashBox>}
      {links.length === 0
        ? <div className="field-hint">Henüz link yok.</div>
        : (
          <table style={{ width: '100%' }}>
            <thead><tr><th style={{ textAlign: 'left' }}>Etiket</th><th style={{ textAlign: 'left' }}>Şablon</th><th style={{ textAlign: 'left' }}>Grup</th><th style={{ textAlign: 'left' }}>Gerekli</th><th style={{ textAlign: 'left' }}>Renk</th><th></th></tr></thead>
            <tbody>
              {links.map((l, i) => (
                <tr key={l.label}>
                  <td>{l.label}</td>
                  <td className="td-full mono" style={{ fontSize: 12 }}>{l.urlTemplate} <CopyButton value={l.urlTemplate} title="Şablonu kopyala" /></td>
                  <td>{l.group?.trim()
                    ? <span className="mono" style={{ fontSize: 12 }}>{l.group.trim()}</span>
                    : <span className="field-hint">tek başına</span>}</td>
                  <td className="field-hint">{(l.requires ?? []).join(', ') || '—'}</td>
                  <td>{l.color
                    ? <span className="mono" style={{ fontSize: 12 }}><span aria-hidden style={{ display: 'inline-block', width: 12, height: 12, borderRadius: 3, background: l.color, verticalAlign: 'middle', marginRight: 4 }} />{l.color}</span>
                    : <span className="field-hint">ikincil</span>}</td>
                  <td><Button variant="ghost-danger" size="sm" disabled={busy} onClick={() => save(links.filter((_, j) => j !== i))}>Sil</Button></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      <Row gap={3} wrap style={{ alignItems: 'flex-end' }}>
        <Field label="Etiket" value={draft.label} onChange={e => setDraft({ ...draft, label: e.target.value })} placeholder="Log İzleme" style={{ width: 180 }} />
        <Field label="URL şablonu" value={draft.urlTemplate} onChange={e => setDraft({ ...draft, urlTemplate: e.target.value })} placeholder={EXAMPLE} className="mono" style={{ width: 620 }} />
        {/* v0.10.346 — düğme dolgu rengi (aracın marka rengi); boş = ikincil düğme. */}
        <Field label="Renk (#rrggbb)" value={draft.color} onChange={e => setDraft({ ...draft, color: e.target.value })} placeholder="#d32f2f" className="mono" style={{ width: 110 }}
          hint={draft.color ? <span aria-hidden style={{ display: 'inline-block', width: 40, height: 10, borderRadius: 3, background: draft.color }} /> : 'boş = gri'} />
        {/* v0.10.566 — grup: aynı gruptan yalnız çözülen ilk link çizilir; boş = tek başına. */}
        <Field label="Grup" value={draft.group} onChange={e => setDraft({ ...draft, group: e.target.value })} placeholder={EXAMPLE_GROUP} className="mono" style={{ width: 150 }}
          hint="aynı gruptan yalnız çözülen ilk link çizilir; boş = tek başına" />
        <Button variant="primary" size="sm" disabled={busy || !draft.label.trim() || !draft.urlTemplate.trim() || (preview?.missing.length ?? 0) > 0}
          onClick={() => save([...links, { label: draft.label.trim(), urlTemplate: draft.urlTemplate.trim(), color: draft.color.trim() || undefined, group: draft.group.trim() || undefined }]).then(() => setDraft({ label: '', urlTemplate: '', color: '', group: '' }))}>
          Ekle
        </Button>
        {/* İki örnek AYNI grupta: birincil request_id, yedek function_id+channel_code. */}
        <Button variant="secondary" size="sm" onClick={() => setDraft({ ...draft, label: draft.label || 'Log İzleme', urlTemplate: EXAMPLE_REQUEST_ID, group: EXAMPLE_GROUP })}>
          Örnek: requestId (birincil)
        </Button>
        <Button variant="secondary" size="sm" onClick={() => setDraft({ ...draft, label: draft.label || 'Log İzleme (functionId)', urlTemplate: EXAMPLE, group: EXAMPLE_GROUP })}>
          Örnek: functionId (yedek)
        </Button>
      </Row>
      {preview && (
        <div className="field-hint">
          Önizleme (örnek değerlerle):{' '}
          {preview.url
            ? <span className="mono" style={{ wordBreak: 'break-all' }}>{preview.url}</span>
            : <span style={{ color: 'var(--err)' }}>çözülemedi — eksik: {preview.missing.join(', ')}</span>}
        </div>
      )}
    </Stack>
  );
}
