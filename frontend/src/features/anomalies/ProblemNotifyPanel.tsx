import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { Spinner, Empty } from '@/components/Spinner';
import { QueryErrorInline } from '@/components/QueryError';
import { tsLong } from '@/lib/utils';
import { summarizeNotifyRouting } from '@/lib/notifyRouting';

// ProblemNotifyPanel — v0.9.1344. "Bu problemden kimin haberi var?"
//
// OPERATÖR RAPORU: hiçbir bildirim kanalıyla eşleşmeyen bir problem
// sessizce düşüyordu ve operatörün bunu ÖĞRENMESİNİN hiçbir yolu yoktu.
// /events tüm defteri gösteriyor ama triyaj eden kişi orada değil —
// açık problemin sayfasında. Cevabın olması gereken yer burası.
//
// ÜÇ HÂL, ÜÇ AYRI ÇİZİM (ikisini birleştirmek yalan olurdu):
//   • işaret var        → KIRMIZI. Yol teklif edildi, kimse almadı.
//     Gerekçe haberin NEREDE kaybolduğunu söyler.
//   • gönderim var      → kime gittiği, sonucuyla.
//   • hiçbiri yok       → "bildirim yok" — KUSUR İDDİASI DEĞİL.
//     Yapılandırılmamış bir kurulumda backend işaret yazmaz, o yüzden
//     boş liste "kimse haber almadı" diye okunmamalı.
//
// Talep üzerine çekilir (problem sayfası açılışı), POLLING YOK: bildirim
// geçmişi bir problemin ömrü boyunca birkaç satır değişir.
export function ProblemNotifyPanel({ problemId }: { problemId: string }) {
  const q = useQuery({
    queryKey: ['problem-notifications', problemId],
    queryFn: () => api.problemNotifications(problemId),
    enabled: !!problemId,
    // Sunucu tarafı 15sn önbellekli; altına inmek boşuna istek.
    staleTime: 15_000,
  });

  if (q.isLoading) return <Spinner />;
  // Hata BOŞ SONUÇ DEĞİL: sessizce "bildirim yok" çizmek, tam da bu
  // sürümün kapattığı sessiz-kayıp sınıfını geri açardı.
  if (q.isError) {
    return <QueryErrorInline text="Bildirim geçmişi okunamadı — bu bir hata, boş sonuç değil."
      onRetry={() => void q.refetch()} />;
  }

  const s = summarizeNotifyRouting(q.data);

  if (!s.unmatched && s.sends.length === 0) {
    return (
      <Empty icon="✉" title="Bu problem için bildirim kaydı yok">
        Ya henüz gönderim olmadı, ya da bu kurulumda bildirim
        yapılandırılmamış. Kanalları Ayarlar → Bildirimler altında
        tanımlayabilirsiniz.
      </Empty>
    );
  }

  return (
    <>
      {s.unmatched && (
        <div style={{
          marginBottom: s.sends.length > 0 ? 10 : 0,
          padding: '8px 10px', borderRadius: 'var(--radius-sm)',
          background: 'var(--err-soft, var(--bg2))',
          border: '1px solid var(--err, var(--border))',
        }}>
          <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
            <span className="badge b-err">KİMSEYE GİTMEDİ</span>
            <span style={{ fontSize: 11, color: 'var(--text3)' }}>
              {tsLong(s.unmatched.sentAt)}
            </span>
          </div>
          {s.unmatched.error && (
            <div style={{
              fontSize: 11.5, color: 'var(--text2)', marginTop: 6, lineHeight: 1.6,
              overflowWrap: 'anywhere',
            }}>
              {s.unmatched.error}
            </div>
          )}
          <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 6 }}>
            Kanalın eşleşme kuralını (servis/küme kapsamı) ya da servis
            kataloğundaki ekip alanını Ayarlar → Bildirimler altından
            genişletin.
          </div>
        </div>
      )}
      {s.sends.length > 0 && (
        <div style={{ fontSize: 11.5, lineHeight: 1.8 }}>
          <div style={{ color: 'var(--text3)', marginBottom: 4 }}>
            {s.okCount > 0 && <>{s.okCount} gönderim başarılı</>}
            {s.okCount > 0 && s.failCount > 0 && ' · '}
            {/* Teslimat arızası AYRI bir sinyal: kanal doğru eşleşti ama
                hedef kabul etmedi. Yönlendirme kusuruyla aynı kovaya
                koymak ikisinin de düzeltmesini gizlerdi. */}
            {s.failCount > 0 && <span className="err">{s.failCount} gönderim başarısız</span>}
          </div>
          {s.sends.map(n => (
            <div key={n.id} style={{ display: 'flex', gap: 8, alignItems: 'baseline' }}>
              <span className={n.ok ? 'badge b-ok' : 'badge b-err'} title={n.error || undefined}>
                {n.ok ? 'gitti' : 'hata'}
              </span>
              <span style={{ color: 'var(--text2)' }}>{n.channelName}</span>
              <span className="mono" style={{
                color: 'var(--text3)', fontSize: 11, overflow: 'hidden',
                textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: 260,
              }} title={n.target}>{n.target || '—'}</span>
              <span style={{ marginLeft: 'auto', color: 'var(--text3)', fontSize: 11, whiteSpace: 'nowrap' }}>
                {tsLong(n.sentAt)}
              </span>
            </div>
          ))}
        </div>
      )}
    </>
  );
}
