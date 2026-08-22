// Self-observability panels for /admin/stats (split out of
// AdminStats.tsx — refactor batch item 2, v0.8.269): ingest
// data-loss counters, Redis INFO, and the multi-tier API cache
// effectiveness. Presentation-only — the page owns the queries and
// hands each panel its (undefined | null | data) tri-state.

import { useState } from 'react';
import { Spinner } from '@/components/Spinner';
import { Button } from '@/components/ui/Button';
import { useDataTable, DataTableHead, DataTableColgroup } from '@/components/DataTable';
import { api } from '@/lib/api';
import { useConfirm } from '@/components/ui/ConfirmDialog';
import { fmtNum, fmtClock } from '@/lib/utils';
import { KPI, fmtUptime, fmtBytes } from './shared';
import type { DataTableColumn } from '@/lib/dataTable';
import type { RedisStats, CacheStats, SystemStats, SpoolState } from '@/lib/types';

type TopKeyRow = CacheStats['topKeys'][number];

// Hottest API cache keys. Default = hits desc (server already returns
// them hottest-first).
const TOPKEY_COLS: DataTableColumn<TopKeyRow>[] = [
  { id: 'key',  label: 'Key',  sortValue: k => k.key,  naturalDir: 'asc',  width: 420 },
  { id: 'hits', label: 'Hits', sortValue: k => k.hits, numeric: true, naturalDir: 'desc', width: 100 },
];

// BehaviorPanel — davranış motorunun (v0.9.936) tik ölçümü.
//
// NEDEN KENDİ KARTI: motorun tek pahalı yanı 28 GÜNLÜK bir MV
// taraması ve prod'da bütçesi 10 saniye. Süre görünmezse "vidaları
// sıkmalı mıyım" sorusunun cevabı da yok; sessizce 20 saniyeye çıkmış
// bir tarama başka hiçbir ekranda iz bırakmazdı.
//
// ÜÇ HÂL, ÜÇ FARKLI CEVAP — hepsi dürüst:
//   alan yok / hiç tik yok → "bu pod taramıyor" (lider başka pod, ya da
//     COREMETRY_MODE=api). Sıfır göstermek YANLIŞ olurdu: sıfır aday ile
//     hiç koşmamak aynı şey değil.
//   lastError dolu        → motor koştu ve PATLADI. Sessiz kapanma bu
//     depoda tekrarlayan hata sınıfı; hata burada görünür.
//   temiz                 → süre + aday + kapsam.
export function BehaviorPanel({ behavior }: { behavior: SystemStats['behavior'] }) {
  const b = behavior;
  const neverRan = !b || b.ticks === 0;
  const slow = !!b && b.lastDurationMs > 10_000;
  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 14, marginBottom: 18,
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: neverRan ? 0 : 12 }}>
        <span style={{ fontSize: 12, fontWeight: 600 }}>Davranış motoru</span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          haftanın saati baseline&apos;ı · 28 gün · anomaly_events kind=behavior_change
        </span>
        <span style={{ flex: 1 }} />
        {neverRan
          ? <span style={{ fontSize: 12, color: 'var(--text3)' }}>bu pod taramıyor</span>
          : b.lastError
            ? <span className="err" style={{ fontSize: 12, fontWeight: 700 }}>⚠ son tarama hata verdi</span>
            : <span className={slow ? 'warn' : 'ok'} style={{ fontSize: 12, fontWeight: 600 }}>
                {slow ? `⚠ ${fmtNum(b.lastDurationMs)} ms` : `✓ ${fmtNum(b.lastDurationMs)} ms`}
              </span>}
      </div>
      {!neverRan && (
        <>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(150px, 1fr))', gap: 12 }}>
            <KPI label="Son tarama" value={b.lastUnix ? fmtUptime(Math.max(0, Math.floor(Date.now() / 1000) - b.lastUnix)) + ' önce' : '—'} />
            {/* v0.9.957 — bütçenin KIRILIMI. "Tik yavaş" tek başına ne
                yapılacağını söylemiyor: yük MV sorgusundaysa vidalar,
                yazımdaysa toplu yazım hattı sorumludur. Alanlar opsiyonel
                (eski backend döndürmez) → yoksa karo hiç çizilmez, sıfır
                göstermek yalan olurdu. */}
            {b.lastQueryMs !== undefined && (
              <KPI label="↳ MV sorgusu" value={`${fmtNum(b.lastQueryMs)} ms`} />
            )}
            {b.lastWriteMs !== undefined && (
              <KPI label="↳ Olay yazımı" value={`${fmtNum(b.lastWriteMs)} ms`} />
            )}
            <KPI label="Son bulgu" value={fmtNum(b.lastCandidates)} />
            <KPI label="Kapsanan servis" value={fmtNum(b.lastServices)} />
            <KPI label="Toplam tarama" value={fmtNum(b.ticks)} />
            <KPI label="Toplam bulgu" value={fmtNum(b.candidates)} />
          </div>
          {b.lastError && (
            <div className="err" style={{
              fontSize: 11, marginTop: 10, fontFamily: 'ui-monospace, monospace',
              overflowWrap: 'anywhere',
            }}>{b.lastError}</div>
          )}
          {/* SESSİZLİĞİN GEREKÇESİ (v0.9.957). Motor 0 bulgu ürettiğinde
              bunun "her şey normal" mi yoksa "henüz öğrenecek kadar geçmiş
              yok" mu olduğunu başka hiçbir ekran söyleyemez. */}
          {!!b.lastScarceBuckets && (
            <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 10, lineHeight: 1.5 }}>
              <b>Yetersiz geçmiş:</b> {fmtNum(b.lastScarceBuckets)} kova atlandı.
              Bu kovalarda henüz güvenilir bir baseline yok (kova başına örnek ya da
              farklı gün sayısı eşiğin altında), o yüzden motor onlar için
              <b> sessiz</b> kalıyor. Kurulum 28 günü doldurdukça kendiliğinden azalır;
              eşikler Settings &rarr; Anomali &rarr; <b>Davranış değişimi</b>.
            </div>
          )}
          {slow && !b.lastError && (
            <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 10, lineHeight: 1.5 }}>
              Tarama 10 saniyelik bütçeyi aştı. Settings &rarr; Anomali &rarr;
              <b> Davranış değişimi</b> bölümünden kalıcılık dilimlerini yükseltmek
              ya da motoru geçici olarak kapatmak yükü düşürür.
            </div>
          )}
        </>
      )}
    </div>
  );
}

// CODE_MISS_LABEL — çıkmaz sınıfının insan-okunur karşılığı
// (backend: devops.CodeOutcome sabitleri). Bilinmeyen bir sınıf ham
// adıyla gösterilir: yeni bir kova eklendiğinde panel onu SAKLAMAZ.
const CODE_MISS_LABEL: Record<string, string> = {
  'unconfigured': 'bağlantı yapılandırılmamış',
  'repo-unresolved': 'depo çözülemedi',
  'project-dead-end': 'proje çözülemedi',
  'no-stack': 'stack yok',
  'catalog-error': 'katalog okunamadı',
  'empty-tree': 'depo ağacı boş',
  'tree-miss': 'dosya ağaçta yok',
  'window-failed': 'pencere kurulamadı',
  'deadline': 'süre tavanı',
  'cancelled': 'istek iptal edildi',
  'backend-error': 'DevOps hatası',
  'other': 'sınıflandırılmamış',
};

// ENTEGRASYON çıkmazları: operatörün AYAR/ERİŞİM düzeltmesi gereken
// sınıflar. no-stack / tree-miss bilerek DIŞARIDA — onlar verinin hâli
// (kayıtta stack yok, dosya o depoda değil), arıza değil. Ayrım
// olmadan sağlıklı bir kurulum sürekli kırmızı yanardı.
const CODE_BROKEN_CLASSES = new Set([
  'unconfigured', 'catalog-error', 'empty-tree', 'backend-error', 'other',
]);

// CodeFetchPanel — "Kodu da incele" kod-çekme sonuçları (v0.9.1241).
//
// NEDEN KENDİ KARTI: bu yol FAIL-OPEN. Kod gelmezse açıklama kodsuz
// üretilir, çekmecede tek satırlık bir gerekçe kalır ve TOPLAMDA
// hiçbir iz olmaz — süresi dolmuş bir PAT tüm filoda kod bağlamını
// sessizce kapatabilir, hiçbir ekran bunu söylemezdi. "Kod bağlamı
// gerçekte ne sıklıkla isabet ediyor" sorusunun cevabı yalnız burada.
//
// ÜÇ HÂL, ÜÇ DÜRÜST CEVAP:
//   alan yok / deneme yok → "hiç denenmedi". %0 İSABET DEMEK YALAN
//     OLURDU: kutuyu hiç işaretlememek ile hep ıskalamak aynı şey değil.
//   entegrasyon çıkmazı   → ⚠ ve gerekçe. Ayar/erişim işi.
//   temiz                 → isabet oranı + kırılım.
//
// SÜREÇ BAŞLANGICINDAN BERİ olduğu başlıkta YAZIYOR: sayaçlar süreç-içi
// ve restart sıfırlar. Kalıcı sayaç yeni bir tablo ve yeni bir yazma
// yolu demekti — bu operatör telemetrisi, faturalama değil.
export function CodeFetchPanel({ code }: { code: SystemStats['codeFetch'] }) {
  const c = code;
  const attempts = c?.attempts ?? 0;
  const neverTried = !c || attempts === 0;
  const misses = c?.misses ?? [];
  const broken = misses.some(m => CODE_BROKEN_CLASSES.has(m.class) && m.count > 0);
  const hits = (c?.ok ?? 0) + (c?.partial ?? 0);
  const rate = attempts > 0 ? Math.round((hits / attempts) * 100) : 0;
  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 14, marginBottom: 18,
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: neverTried ? 0 : 12 }}>
        <span style={{ fontSize: 12, fontWeight: 600 }}>CoSRE kod bağlamı</span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          &quot;Kodu da incele&quot; · DevOps kaynak penceresi · süreç başlangıcından beri
          (restart sıfırlar)
        </span>
        <span style={{ flex: 1 }} />
        {neverTried
          ? <span style={{ fontSize: 12, color: 'var(--text3)' }}>hiç denenmedi</span>
          : broken
            ? <span className="err" style={{ fontSize: 12, fontWeight: 700 }}>⚠ %{rate} isabet</span>
            : <span className="ok" style={{ fontSize: 12, fontWeight: 600 }}>✓ %{rate} isabet</span>}
      </div>
      {!neverTried && c && (
        <>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(150px, 1fr))', gap: 12 }}>
            <KPI label="Deneme" value={fmtNum(attempts)} />
            <KPI label="İsabet" value={`%${rate}`} cls={broken ? 'err' : 'ok'}
                 sub={`${fmtNum(hits)} / ${fmtNum(attempts)}`} />
            <KPI label="Tam" value={fmtNum(c.ok)} />
            {/* Kısmi = pencere geldi ama eksik (bütçe kesti, bir frame
                ıskalandı, tavan doldu). Tam ile aynı kovaya koymak
                isabeti olduğundan iyi gösterirdi. */}
            <KPI label="Kısmi" value={fmtNum(c.partial)} />
            <KPI label="Son deneme"
                 value={c.lastUnix ? fmtUptime(Math.max(0, Math.floor(Date.now() / 1000) - c.lastUnix)) + ' önce' : '—'}
                 sub={c.lastOutcome ? (CODE_MISS_LABEL[c.lastOutcome] ?? c.lastOutcome) : undefined} />
          </div>
          {misses.length > 0 && (
            <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 10, lineHeight: 1.7 }}>
              <b>Çıkmazlar:</b>{' '}
              {misses.map((m, i) => (
                <span key={m.class}>
                  {i > 0 && ' · '}
                  <span className={CODE_BROKEN_CLASSES.has(m.class) ? 'err' : undefined}>
                    {CODE_MISS_LABEL[m.class] ?? m.class}
                  </span>
                  {' '}{fmtNum(m.count)}
                </span>
              ))}
            </div>
          )}
          {/* Son BAŞARISIZ denemenin gerekçesi. YAPIŞKAN: sonraki bir
              başarı silmez — flap eden bir arızayı tek şanslı isabet
              ekrandan silerse operatör onu hiç görmez. Tazeliği
              zaman damgası anlatır. */}
          {c.lastError && (
            <div className="err" style={{
              fontSize: 11, marginTop: 10, fontFamily: 'ui-monospace, monospace',
              overflowWrap: 'anywhere',
            }}>
              {c.lastError}
              {!!c.lastErrorUnix && (
                <span style={{ color: 'var(--text3)', fontFamily: 'inherit' }}>
                  {' '}· {fmtUptime(Math.max(0, Math.floor(Date.now() / 1000) - c.lastErrorUnix))} önce
                </span>
              )}
            </div>
          )}
          {broken && (
            <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 10, lineHeight: 1.5 }}>
              Çıkmazların bir kısmı ayar/erişim kaynaklı (bağlantı, PAT yetkisi,
              proje/depo adı). Settings &rarr; <b>Kod entegrasyonu</b> ve servis
              kataloğundaki <b>Repository</b> alanı bu kovaları kapatır.
            </div>
          )}
        </>
      )}
    </div>
  );
}

// DistributionQueuePanel — dağıtık kipte Distributed spool derinliği
// (v0.9.985).
//
// NEDEN KENDİ KARTI: bu, "ingest data loss" kartının GÖREMEDİĞİ kayıptır.
// Distributed motoru INSERT'i diske spool'layıp hemen OK döner; asıl
// gönderim arka planda *_local'a olur. O gönderici takıldığında yazma
// yolundaki her sayaç temiz kalır — 2026-08-12'de lokal küme 3s39d
// boyunca tek span yazamazken spans_write_failed 0'dı, spans_accepted
// tırmanıyordu ve /api/health "ok" diyordu. Kayıp yalnız burada görünür.
//
// TEK-DÜĞÜM: alan hiç gelmez, kart hiç çizilmez (Distributed tablo yok,
// spool yok, sorgu bile çalışmadı).
//
// ÜÇ HÂL, ÜÇ DÜRÜST CEVAP:
//   measured=false → "ölçülemedi". SIFIR GÖSTERMEK YALAN OLURDU: düşen
//     bir probe de files=0 üretir (v0.9.984 fail-open dersi).
//   derinlik eşik altı → ✓ temiz. Anlık spool normaldir.
//   derinlik eşik üstü → tablo başına kırılım + CH'nin kendi istisnası.
//
// Kartın TREND İDDİASI YOKTUR (yön iki ardışık ölçüm ister; onu
// /api/health 30 sn'lik nabzıyla yürütür). Burada söylenen tek şey
// derinliğin ne olduğu — panelin bilmediği bir şeyi iddia etmiyor.
export function DistributionQueuePanel({ dq }: { dq: SystemStats['distributionQueue'] }) {
  if (!dq) return null;   // tek düğüm: kavram yok
  // 100 = backend'deki distributedBacklogFloor. Canlı ölçüm: sağlam
  // tablolar 0-1 dosya, kilitlenmiş olanlar 12.702 / 26.132.
  const deep = dq.measured && dq.files >= 100;
  const rows = (dq.tables ?? []).filter(t => t.files > 0 || t.errorCount > 0 || t.brokenFiles > 0);
  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 14, marginBottom: 18,
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: deep || !dq.measured ? 12 : 0 }}>
        <span style={{ fontSize: 12, fontWeight: 600 }}>Distributed spool</span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          system.distribution_queue · INSERT&apos;in &quot;OK&quot; dönmesi verinin indiği anlamına gelmez
          {dq.partial && ' · yalnız bu düğüm'}
        </span>
        <span style={{ flex: 1 }} />
        {!dq.measured
          ? <span className="warn" style={{ fontSize: 12, fontWeight: 700 }}>⚠ ölçülemedi</span>
          : deep
            ? <span className="err" style={{ fontSize: 12, fontWeight: 700 }}>
                ⚠ {fmtNum(dq.files)} dosya bekliyor
              </span>
            : <span className="ok" style={{ fontSize: 12, fontWeight: 600 }}>✓ birikme yok</span>}
      </div>

      {/* "Ölçemedim" ≠ "temiz" — sıfırları göstermek yerine nedeni göster. */}
      {!dq.measured && (
        <div style={{ fontSize: 11, color: 'var(--text3)', lineHeight: 1.5 }}>
          Spool derinliği okunamadı, yani bu kutu <b>hiçbir şey kanıtlamıyor</b> —
          birikme olabilir de olmayabilir de.
          {dq.probeError && (
            <div className="err" style={{
              marginTop: 6, fontFamily: 'ui-monospace, monospace', overflowWrap: 'anywhere',
            }}>{dq.probeError}</div>
          )}
        </div>
      )}

      {dq.measured && deep && (
        <>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(150px, 1fr))', gap: 12 }}>
            <KPI label="Bekleyen dosya" value={fmtNum(dq.files)} cls="err" />
            <KPI label="Spool boyutu" value={fmtBytes(dq.bytes)} />
            <KPI label="Gönderim hatası" value={fmtNum(dq.errorCount)}
                 sub="sunucu açılışından beri kümülatif" />
            {dq.brokenFiles > 0 && (
              <KPI label="Bozuk dosya" value={fmtNum(dq.brokenFiles)} cls="err"
                   sub="kalıcı olarak kenara kondu — yeniden denenmez" />
            )}
          </div>
          {rows.length > 0 && (
            <div style={{ marginTop: 12, display: 'flex', flexDirection: 'column', gap: 6 }}>
              {rows.map(t => (
                <div key={t.table} style={{ fontSize: 11, lineHeight: 1.5 }}>
                  <b>{t.table}</b>
                  <span style={{ color: 'var(--text3)' }}>
                    {' · '}{fmtNum(t.files)} dosya · {fmtBytes(t.bytes)}
                    {t.errorCount > 0 && ` · ${fmtNum(t.errorCount)} hata`}
                    {t.brokenFiles > 0 && ` · ${fmtNum(t.brokenFiles)} bozuk`}
                  </span>
                  {t.lastError && (
                    <div className="err" style={{
                      fontFamily: 'ui-monospace, monospace', marginTop: 2,
                      overflowWrap: 'anywhere', fontSize: 10,
                    }}>{t.lastError}</div>
                  )}
                </div>
              ))}
            </div>
          )}
          {/* Kısmi ölçüm İTİRAF edilir: sessiz bir kırpma "küme geneli
              toplam bu" diye okunur ve derinliği olduğundan küçük gösterir. */}
          {dq.partial && (
            <div className="warn" style={{ fontSize: 11, marginTop: 10, lineHeight: 1.5 }}>
              Küme geneli okuma düştüğü için bu sayılar <b>yalnız bir düğümün</b> spool&apos;u —
              gerçek toplam daha yüksek.
            </div>
          )}
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 10, lineHeight: 1.5 }}>
            ClickHouse INSERT&apos;leri kabul edip diske yazıyor ama arka plan göndericisi
            <code> *_local</code> tablolarına basamıyor. Bu birikme <b>erimiyorsa</b> veri
            hiç inmiyor demektir — yazma yolundaki sayaçlar (write failed / queue) bu
            arızayı tanım gereği göremez. Son hata satırı nedeni söyler: <code>241</code>
            {' '}bellek tavanı, <code>159</code> zaman aşımı.
          </div>
          <SpoolRunbook />
        </>
      )}
    </div>
  );
}

// DropsPanel renders the cumulative ingest data-loss counters (since process
// start). Compact "✓ no loss" when clean; a red per-signal breakdown when any
// counter is non-zero — queue-full (receiver buffer overflow) vs write-failed
// (ClickHouse insert dropped, not retried). Self-observability: an explicit
// "no loss" indicator is as valuable as the alarm.
export function DropsPanel({ drops }: { drops: SystemStats['drops'] }) {
  const d = drops ?? {
    spansQueueFull: 0, logsQueueFull: 0, metricsQueueFull: 0,
    spansWriteFailed: 0, logsWriteFailed: 0, metricsWriteFailed: 0,
    spansPipeline: 0, logsPipeline: 0, metricsPipeline: 0,
  };
  const total =
    d.spansQueueFull + d.logsQueueFull + d.metricsQueueFull +
    d.spansWriteFailed + d.logsWriteFailed + d.metricsWriteFailed;
  // Pipeline drops are INTENTIONAL (operator drop/sample rules) — kept out
  // of the loss `total`/alarm and shown as a neutral informational line so
  // an active drop rule never flips the "✓ no loss" indicator (v0.8.282).
  const pipelineTotal = d.spansPipeline + d.logsPipeline + d.metricsPipeline;
  const signals = [
    { label: 'Spans',   queueFull: d.spansQueueFull,   writeFailed: d.spansWriteFailed },
    { label: 'Logs',    queueFull: d.logsQueueFull,    writeFailed: d.logsWriteFailed },
    { label: 'Metrics', queueFull: d.metricsQueueFull, writeFailed: d.metricsWriteFailed },
  ];
  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 14, marginBottom: 18,
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: total > 0 ? 12 : 0 }}>
        <span style={{ fontSize: 12, fontWeight: 600 }}>Ingest data loss</span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          cumulative since process start · queue-full = buffer overflow · write-failed = CH insert dropped
        </span>
        <span style={{ flex: 1 }} />
        {total === 0
          ? <span className="ok" style={{ fontSize: 12, fontWeight: 600 }}>✓ no loss</span>
          : <span className="err" style={{ fontSize: 12, fontWeight: 700 }}>⚠ {fmtNum(total)} dropped</span>}
      </div>
      {total > 0 && (
        <div style={{
          display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(180px, 1fr))', gap: 12,
        }}>
          {signals.map(s => {
            const lost = s.queueFull + s.writeFailed;
            return (
              <div key={s.label} style={{
                padding: 12, border: '1px solid var(--border)',
                borderRadius: 6, background: 'var(--bg2)',
              }}>
                <div style={{
                  fontSize: 10, color: 'var(--text3)',
                  textTransform: 'uppercase', letterSpacing: 0.4, fontWeight: 600,
                }}>{s.label}</div>
                <div className={lost > 0 ? 'err' : 'ok'} style={{ fontSize: 20, fontWeight: 700, marginTop: 4 }}>
                  {fmtNum(lost)}
                </div>
                <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 2 }}>
                  queue-full {fmtNum(s.queueFull)} · write-failed {fmtNum(s.writeFailed)}
                </div>
              </div>
            );
          })}
        </div>
      )}
      {pipelineTotal > 0 && (
        <div style={{
          marginTop: total > 0 ? 12 : 8, paddingTop: 8,
          borderTop: total > 0 ? '1px solid var(--border)' : 'none',
          fontSize: 11, color: 'var(--text3)',
        }}>
          Dropped by pipeline rules (intentional): spans {fmtNum(d.spansPipeline)}
          {' · '}logs {fmtNum(d.logsPipeline)} · metrics {fmtNum(d.metricsPipeline)}
        </div>
      )}
    </div>
  );
}

// RedisPanel renders Redis INFO + DBSIZE — keys, memory, hit-rate,
// ops/sec — alongside the ClickHouse storage table. Falls back to
// "Redis not configured" when version is empty (server returned a
// zero-valued struct because no Redis URL is wired). Polled every
// 10s so the ops/sec gauge feels live during incident response.
export function RedisPanel({ data }: { data: RedisStats | null | undefined }) {
  if (data === undefined) {
    return (
      <div style={{
        background: 'var(--bg1)', border: '1px solid var(--border)',
        borderRadius: 8, padding: 14, marginBottom: 18,
      }}>
        <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 10 }}>
          Redis cache <span style={{ color: 'var(--text3)', fontWeight: 400 }}>· loading…</span>
        </div>
        <Spinner />
      </div>
    );
  }
  if (data === null) {
    return (
      <div style={{
        background: 'var(--bg1)', border: '1px solid var(--border)',
        borderRadius: 8, padding: 14, marginBottom: 18,
      }}>
        <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 10 }}>
          Redis cache <span style={{ color: 'var(--err)', fontWeight: 400 }}>· probe failed</span>
        </div>
        <div style={{ fontSize: 12, color: 'var(--text2)' }}>
          INFO command returned an error. Check the Redis URL in config or container logs.
        </div>
      </div>
    );
  }
  if (!data.version) {
    return (
      <div style={{
        background: 'var(--bg1)', border: '1px solid var(--border)',
        borderRadius: 8, padding: 14, marginBottom: 18,
      }}>
        <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 8 }}>
          Redis cache <span style={{ color: 'var(--text3)', fontWeight: 400 }}>· not configured</span>
        </div>
        <div style={{ fontSize: 12, color: 'var(--text2)', lineHeight: 1.6 }}>
          Coremetry is running with the in-memory Noop cache. For multi-replica HA
          (alert deduplication, response cache shared across pods, anomaly
          evaluator leader election) wire <code>cache.redis_url</code> in the config or
          set <code>COREMETRY_REDIS_URL=redis://&lt;host&gt;:6379/0</code> in the
          environment.
        </div>
      </div>
    );
  }
  const memPct = data.maxMemoryBytes > 0
    ? (data.usedMemoryBytes / data.maxMemoryBytes) * 100
    : 0;
  const evicting = data.evictedKeys > 0;
  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 14, marginBottom: 18,
    }}>
      <div style={{
        display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 12,
      }}>
        <span style={{ fontSize: 12, fontWeight: 600 }}>Redis cache</span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          v{data.version} · {data.mode || 'standalone'} · uptime {fmtUptime(data.uptimeSec)}
        </span>
      </div>
      <div style={{
        display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(170px, 1fr))', gap: 10,
      }}>
        <KPI label="Keys"          value={fmtNum(data.keys)} />
        <KPI label="Hit rate"      value={`${(data.hitRate * 100).toFixed(1)}%`}
             cls={data.hitRate >= 0.8 ? 'ok' : data.hitRate >= 0.5 ? 'warn' : 'err'} />
        <KPI label="Ops / sec"     value={fmtNum(Math.round(data.opsPerSec))} />
        <KPI label="Clients"       value={String(data.connectedClients)} />
        <KPI label="Memory"
             value={fmtBytes(data.usedMemoryBytes)}
             sub={data.maxMemoryBytes > 0
               ? `${memPct.toFixed(0)}% of ${fmtBytes(data.maxMemoryBytes)}`
               : `peak ${fmtBytes(data.usedMemoryPeakBytes)}`}
             cls={memPct >= 90 ? 'err' : memPct >= 75 ? 'warn' : undefined} />
        <KPI label="Net in"        value={`${data.netInputKbps.toFixed(1)} KB/s`} />
        <KPI label="Net out"       value={`${data.netOutputKbps.toFixed(1)} KB/s`} />
        <KPI label="Evicted"
             value={fmtNum(data.evictedKeys)}
             cls={evicting ? 'warn' : undefined}
             sub={evicting ? 'maxmemory pressure' : undefined} />
        <KPI label="Expired"       value={fmtNum(data.expiredKeys)} />
      </div>
    </div>
  );
}

// ApiCachePanel renders the multi-tier API cache effectiveness:
// per-tier hit distribution as a stacked bar, KPI tiles for the
// computed hit rate / total requests / L1 fill, and the top
// 20 hottest keys. Polled every 10s so the operator can see
// the cache warming up after a deploy. Self-hides with a
// "cache idle" tile when no requests have been served yet
// (fresh process, no traffic).
const TIER_ORDER = ['HIT-L1', 'HIT', 'STALE', 'HIT-LEGACY', 'MISS', 'BYPASS'] as const;
const TIER_COLOR: Record<string, string> = {
  'HIT-L1':     'var(--ok)',     // green — best, no network
  'HIT':        'var(--teal)',   // teal — Redis fresh
  'STALE':      'var(--warn)',   // amber — served stale, refresh fired
  'HIT-LEGACY': 'var(--text3)',  // grey — pre-envelope entry
  'MISS':       'var(--err)',    // red — upstream hit
  'BYPASS':     'var(--purple)', // purple — operator forced refresh
};
export function ApiCachePanel({ data }: { data: CacheStats | null | undefined }) {
  // Shared sortable + resizable hot-keys table — hook BEFORE the
  // early returns below (react-hooks rules-of-hooks).
  const topKeysDt = useDataTable<TopKeyRow>({
    storageKey: 'adminstats-topkeys', columns: TOPKEY_COLS,
    rows: data?.topKeys ?? [], initialSort: { id: 'hits', dir: 'desc' },
  });
  if (data === undefined) {
    return (
      <div style={{
        background: 'var(--bg1)', border: '1px solid var(--border)',
        borderRadius: 8, padding: 14, marginBottom: 18,
      }}>
        <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 10 }}>
          API cache <span style={{ color: 'var(--text3)', fontWeight: 400 }}>· loading…</span>
        </div>
        <Spinner />
      </div>
    );
  }
  if (data === null) {
    return (
      <div style={{
        background: 'var(--bg1)', border: '1px solid var(--border)',
        borderRadius: 8, padding: 14, marginBottom: 18,
      }}>
        <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 10 }}>
          API cache <span style={{ color: 'var(--err)', fontWeight: 400 }}>· probe failed</span>
        </div>
      </div>
    );
  }
  const counts = data.counts || {};
  const total = TIER_ORDER.reduce((acc, t) => acc + (counts[t] ?? 0), 0);
  const hits = ['HIT-L1', 'HIT', 'STALE', 'HIT-LEGACY']
    .reduce((acc, t) => acc + (counts[t] ?? 0), 0);
  const hitRate = total > 0 ? (hits / total) * 100 : 0;
  const sinceMs = (Date.now() * 1_000_000 - data.sinceUnixNano) / 1_000_000;
  return (
    <div style={{
      background: 'var(--bg1)', border: '1px solid var(--border)',
      borderRadius: 8, padding: 14, marginBottom: 18,
    }}>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, marginBottom: 12 }}>
        <span style={{ fontSize: 12, fontWeight: 600 }}>API cache</span>
        <span style={{ fontSize: 11, color: 'var(--text3)' }}>
          L1 · Redis · singleflight · SWR — since {fmtUptime(Math.floor(sinceMs / 1000))} ago
        </span>
      </div>

      {total === 0 ? (
        <div style={{ fontSize: 12, color: 'var(--text2)' }}>
          No cached endpoints served yet since process start. Hit any
          dashboard / services page to populate.
        </div>
      ) : (
        <>
          {/* KPI tiles */}
          <div style={{
            display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(140px, 1fr))',
            gap: 10, marginBottom: 14,
          }}>
            <KPI label="Hit rate" value={`${hitRate.toFixed(1)}%`}
              cls={hitRate < 50 ? 'warn' : undefined}
              sub={hitRate < 50 ? 'cold cache — most requests miss' : undefined} />
            <KPI label="Total requests" value={fmtNum(total)} />
            <KPI label="L1 (in-process)" value={`${data.l1Size} / ${data.l1Cap}`}
              sub={data.l1Size >= data.l1Cap ? 'at cap — eviction active' : undefined} />
            <KPI label="Stale refreshes" value={fmtNum(counts['STALE'] ?? 0)}
              sub="served immediately + background refresh fired" />
          </div>

          {/* Stacked tier-distribution bar */}
          <div style={{ marginBottom: 14 }}>
            <div style={{
              display: 'flex', height: 14, borderRadius: 4, overflow: 'hidden',
              border: '1px solid var(--border)',
            }}>
              {TIER_ORDER.map(tier => {
                const n = counts[tier] ?? 0;
                const pct = total > 0 ? (n / total) * 100 : 0;
                if (pct === 0) return null;
                return (
                  <div key={tier} title={`${tier}: ${fmtNum(n)} (${pct.toFixed(1)}%)`}
                    style={{ width: `${pct}%`, background: TIER_COLOR[tier] }} />
                );
              })}
            </div>
            <div style={{
              display: 'flex', flexWrap: 'wrap', gap: 12,
              fontSize: 11, color: 'var(--text2)', marginTop: 8,
            }}>
              {TIER_ORDER.map(tier => {
                const n = counts[tier] ?? 0;
                if (n === 0) return null;
                const pct = total > 0 ? (n / total) * 100 : 0;
                return (
                  <span key={tier} style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
                    <span style={{
                      display: 'inline-block', width: 10, height: 10,
                      borderRadius: 2, background: TIER_COLOR[tier],
                    }} />
                    <span style={{ fontFamily: 'ui-monospace, monospace' }}>{tier}</span>
                    <span style={{ color: 'var(--text3)' }}>
                      {fmtNum(n)} · {pct.toFixed(1)}%
                    </span>
                  </span>
                );
              })}
            </div>
          </div>

          {/* Top hot keys */}
          {data.topKeys && data.topKeys.length > 0 && (
            <div>
              <div style={{ fontSize: 11, fontWeight: 600, color: 'var(--text2)', marginBottom: 6 }}>
                Hottest cache keys
              </div>
              <div className="table-wrap">
                <table style={{ fontSize: 12, tableLayout: 'fixed', width: '100%' }}>
                  <DataTableColgroup dt={topKeysDt} />
                  <DataTableHead dt={topKeysDt} />
                  <tbody>
                    {topKeysDt.sortedRows.map(k => (
                      <tr key={k.key}>
                        <td style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11 }}>
                          {k.key}
                        </td>
                        <td className="num">{fmtNum(k.hits)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}


// ── SpoolRunbook (v0.9.1191) — "Runbook sen otomatik ekle, ben çalıştırayım" ──
//
// Operatör isteğinin birebir hâli: teşhis yukarıdaki kartta zaten vardı;
// bu blok MÜDAHALEYİ ekler. Veri yalnız "Runbook'u aç"a basılınca çekilir
// (fetch-on-open disiplini) ve poll YOKTUR — ilerlemenin gerçek göstergesi
// zaten 30 sn'de bir ölçülen spool derinliği; buradaki "Yenile" düğmesi
// operatörün elinde.
//
// İki düğme de admin-only uçlara gider ve audit düşer. FLUSH asenkron
// başlar: saatler sürebilir, durum uçuş defterinde (flights) görünür.
// Bellek tavanı ve spool dosyası silme adımları BİLEREK düğme değil metin:
// biri k8s pod limiti, öteki veri kaybı — uygulamanın basacağı düğmeler
// değil.
function SpoolRunbook() {
  const [state, setState] = useState<SpoolState | null | undefined>(undefined);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState('');
  const [note, setNote] = useState('');
  const confirm = useConfirm();

  const load = () => {
    api.adminSpool().then(setState).catch(() => setState(null));
  };
  const act = async (kind: 'flush' | 'start', table: string) => {
    if (kind === 'flush' && !await confirm({
      title: `${table} spool'u zorla boşaltılsın mı?`,
      body: `SYSTEM FLUSH DISTRIBUTED ${table} — kuyruğu senkron boşaltır; derin bir spool'da saatler sürebilir. İşlem arka planda koşar, ilerleme yukarıdaki dosya sayısından izlenir.`,
      confirmLabel: 'Zorla boşalt',
    })) return;
    setBusy(kind + ':' + table); setNote('');
    try {
      if (kind === 'flush') {
        await api.adminSpoolFlush(table);
        setNote(`${table}: flush başladı — ilerleme yukarıdaki dosya sayısından izlenir.`);
      } else {
        await api.adminSpoolStartSends(table);
        setNote(`${table}: gönderici başlatıldı (zaten açıksa zararsız).`);
      }
    } catch (e) {
      setNote(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(''); load();
    }
  };

  if (!open) {
    return (
      <div style={{ marginTop: 10 }}>
        <Button variant="secondary" size="sm" onClick={() => { setOpen(true); load(); }}>
          Runbook&apos;u aç
        </Button>
      </div>
    );
  }
  return (
    <div style={{ marginTop: 12, borderTop: '1px solid var(--border)', paddingTop: 10 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
        <span style={{ fontSize: 12, fontWeight: 600 }}>Runbook</span>
        <span style={{ flex: 1 }} />
        <Button variant="ghost" size="sm" onClick={load}>Yenile</Button>
      </div>
      {state === undefined && <Spinner />}
      {state === null && <div className="err" style={{ fontSize: 11 }}>Durum okunamadı.</div>}
      {state && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10, fontSize: 11, lineHeight: 1.5 }}>
          {/* 0 — disk: spool ~1 TiB'ken önce taşma riski cevaplanır. */}
          <div>
            <b>0 · Disk</b>
            {state.disksError && <span className="err"> — okunamadı: {state.disksError}</span>}
            {(state.disks ?? []).map(d => {
              const pct = d.total > 0 ? (d.free / d.total) * 100 : 0;
              return (
                <div key={d.host + d.disk} style={{ color: pct < 15 ? 'var(--err)' : 'var(--text3)' }}>
                  {d.host} · {d.disk}: {fmtBytes(d.free)} boş / {fmtBytes(d.total)} ({pct.toFixed(0)}%)
                  {pct < 15 && <b> — KRİTİK: disk dolarsa spans/logs dahil her şey durur</b>}
                </div>
              );
            })}
          </div>
          {/* 1 — tablo başına eylemler. Hedef listesi sunucunun kendi
              Distributed envanteri; elle ad girilemez. */}
          <div>
            <b>1 · Eylemler</b> <span style={{ color: 'var(--text3)' }}>(admin; her biri audit&apos;e düşer)</span>
            {(state.tables ?? []).map(t => {
              const fl = state.flights.find(f => f.table === t && !f.doneAt);
              const done = state.flights.find(f => f.table === t && f.doneAt);
              return (
                <div key={t} style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 4, flexWrap: 'wrap' }}>
                  <code style={{ minWidth: 140 }}>{t}</code>
                  <Button variant="secondary" size="sm" disabled={busy !== ''}
                    onClick={() => void act('start', t)}>Göndericiyi başlat</Button>
                  <Button variant="ghost-danger" size="sm" disabled={busy !== '' || !!fl}
                    onClick={() => void act('flush', t)}>Zorla boşalt (FLUSH)</Button>
                  {fl && <span className="warn">flush koşuyor ({fmtClock(new Date(fl.startedAt / 1e6))})</span>}
                  {!fl && done?.error && <span className="err" title={done.error}>son flush hata verdi — tekrar başlatmak kaldığı yerden sürer</span>}
                  {!fl && done && !done.error && <span className="ok">son flush tamamlandı</span>}
                </div>
              );
            })}
          </div>
          {/* 2+3 — düğmesi OLMAYAN adımlar; runbook yine de tam olsun. */}
          <div style={{ color: 'var(--text3)' }}>
            <b style={{ color: 'var(--text)' }}>2 · Bellek tavanı</b> — flush yine <code>241</code> ile
            düşüyorsa CH pod limiti + <code>max_server_memory_usage</code> birlikte yükseltilmeli
            (yalnız biri yetmez). <b style={{ color: 'var(--text)' }}>3 · Son çare</b> — spool
            dosyalarını elle silmek kuyruktaki veriyi KAYBEDER; bu karar buradan verilemez,
            düğmesi bilerek yok.
          </div>
          {note && <div style={{ fontWeight: 600 }}>{note}</div>}
        </div>
      )}
    </div>
  );
}
