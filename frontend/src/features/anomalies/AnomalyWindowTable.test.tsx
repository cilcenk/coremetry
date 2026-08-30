// AnomalyWindowTable.test.tsx — v0.10.162: penceredeki anomaliler tablosu
// çalışma zamanında — satır başına tür/tepe/durum, sessiz satır rozeti
// (iki parmak izi yazımı), «Değil» yalnız düzenleyici + sessiz olmayan
// satırda, boş liste → hiç çizilmez, kesik liste altyazısı.
import { describe, it, expect } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { AnomalyWindowTable } from './AnomalyWindowTable';
import type { AnomalyEvent, AnomalySilence } from '@/lib/types';

const S = 1e9;
const ev = (over: Partial<AnomalyEvent>): AnomalyEvent => ({
  id: 'sha1', kind: 'trace_op', pattern: 'POST /v1/charges', service: 'payments-orchestrator',
  startedAt: 1_700_000_000 * S, lastSeen: 1_700_000_600 * S, peakRatio: 4.2, currentRatio: 3.1, currentCount: 12, sample: '', status: 'active', ...over,
});
// createdAt/untilAt unix NS (anomaly_silence.go) — inceleme blocker: saniye sanılıp ×1000 ile Invalid Date basılıyordu.
const sil = (fp: string): AnomalySilence => ({ id: 's', fingerprint: fp, kind: 'trace_op', pattern: '', service: '', createdBy: 'cenk', createdAt: 1_700_000_000 * 1e9, untilAt: 1_700_600_000 * 1e9, reason: '', active: true });
const html = (p: Omit<Parameters<typeof AnomalyWindowTable>[0], 'onVerdict'> & { onVerdict?: Parameters<typeof AnomalyWindowTable>[0]['onVerdict'] }) => renderToStaticMarkup(<MemoryRouter><AnomalyWindowTable {...p} onVerdict={p.onVerdict ?? (() => {})} /></MemoryRouter>);
const noop = () => {};

describe('AnomalyWindowTable (v0.10.162)', () => {
  it('satır: tür, tepe, durum; düzenleyiciye «Değil → sessize al»', () => {
    const h = html({ events: [ev({}), ev({ id: 'x2', kind: 'log_pattern', status: 'cleared', peakRatio: 6.1 })], silences: [], canEdit: true, onOpen: noop, onMute: noop, truncated: false });
    expect(h).toContain('trace_op');
    expect(h).toContain('×4.2');
    expect(h).toContain('×6.1');
    expect(h).toContain('active');
    expect(h).toContain('cleared');
    expect(h.match(/Değil → sessize al/g)?.length).toBe(2);
  });
  it('sessiz satır: rozet, «Değil» yok — sha1 id ya da kind|pattern|service anahtarıyla', () => {
    const h1 = html({ events: [ev({})], silences: [sil('sha1')], canEdit: true, onOpen: noop, onMute: noop, truncated: false });
    expect(h1).toContain('sessiz');
    expect(h1).not.toContain('NaN');
    expect(h1).toContain('2023'); // untilAt ns → gerçek tarih
    expect(h1).not.toContain('Değil → sessize al');
    const h2 = html({ events: [ev({})], silences: [sil('trace_op|POST /v1/charges|payments-orchestrator')], canEdit: true, onOpen: noop, onMute: noop, truncated: false });
    expect(h2).toContain('sessiz');
  });
  it('görüntüleyici «Değil» görmez; boş liste hiç çizilmez; kesik altyazısı', () => {
    const h = html({ events: [ev({})], silences: [], canEdit: false, onOpen: noop, onMute: noop, truncated: true });
    expect(h).not.toContain('Değil → sessize al');
    expect(h).toContain('KESİLDİ');
    expect(html({ events: [], silences: [], canEdit: true, onOpen: noop, onMute: noop, truncated: false })).toBe('');
  });
});

// v0.10.181 — karar sütunu ve düğmeleri
describe('AnomalyWindowTable karar (v0.10.181)', () => {
  it('kararsız satırda «Anomali» + «Değil → sessize al»; anomali kararında yalnız Değil; değil kararında yalnız Anomali + rozet', () => {
    const h = html({ events: [ev({}), ev({ id: 'v1', verdict: 'anomaly', verdictBy: 'cenk', verdictAt: 1_700_000_100 * S }), ev({ id: 'v2', verdict: 'not_anomaly', verdictBy: 'cenk' })], silences: [], canEdit: true, onOpen: noop, onMute: noop, truncated: false });
    expect(h.match(/>Anomali</g)?.length).toBe(2);
    expect(h.match(/Değil → sessize al/g)?.length).toBe(2);
    expect(h).toContain('anomali ✓');
    expect(h).toContain('değil ✗');
    expect(h).toContain('cenk');
  });
  it('viewer düğme görmez, kararı görür', () => {
    const h = html({ events: [ev({ verdict: 'anomaly' })], silences: [], canEdit: false, onOpen: noop, onMute: noop, truncated: false });
    expect(h).toContain('anomali ✓');
    expect(h).not.toContain('>Anomali<');
  });
});

// v0.10.184 — susturulmuş olayda «Değil» (karar) düğmesi
describe('AnomalyWindowTable susturulmuş + karar (v0.10.184)', () => {
  it('sessiz satırda snooze yok ama «Değil» kararı var; değil kararı verilmişse yok', () => {
    const h = html({ events: [ev({}), ev({ id: 'v2', verdict: 'not_anomaly' })], silences: [sil('sha1'), sil('v2')], canEdit: true, onOpen: noop, onMute: noop, truncated: false });
    expect(h).not.toContain('Değil → sessize al');
    expect(h.match(/>Değil</g)?.length).toBe(1);
  });
});
