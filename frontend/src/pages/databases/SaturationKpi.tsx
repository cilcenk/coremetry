import { useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { worstSaturation, saturationLabel, saturationTone } from '@/lib/dbSaturation';
import type { DBSaturation } from '@/lib/types';

// SaturationKpi — KOŞULLU havuz doygunluğu karosu (v0.9.822).
//
// Bu ölçüler v0.7'den beri VARDI ama yalnız alert evaluator'ı okuyordu:
// operatör "oturum havuzu dolmak üzere" bilgisini ancak bir Problem
// AÇILDIKTAN sonra görüyordu; o soruyu sormak için gidilen sayfa hiç
// bilmiyordu.
//
// KOŞULLU, "boş hâlli" DEĞİL. Hiç gauge yoksa bu bileşen null döner ve
// şerit üç karo olarak kalır — dördüncü bir yer tutucu ÇİZİLMEZ.
// Gerekçe /messaging'in consumer-lag karosunu HİÇ KURMAMA kararıyla
// aynı (messaging_series.go): uydurulmuş bir "%0 doygunluk", ölçülmemiş
// bir şeyi ölçülmüş gibi gösterirdi ve bu sayfanın bütün serisi tam da
// o sınıfı temizliyor.
//
// Karo TIKLANABİLİR: en dar havuzun instance'ı, receiver panelinin
// bulunduğu satır çekmecesini açar. Doygunluğun DETAYI (oturum grafiği,
// wait class'ları, tablespace tablosu) zaten orada yaşıyor; karo o
// panele bir kapı, ikinci bir gösterge değil.
export function SaturationKpi({ onFocusInstance }: {
  /** En dar havuzun (system, instance) satır çekmecesini açar. */
  onFocusInstance: (system: string, instance: string) => void;
}) {
  const q = useQuery({
    queryKey: ['db-saturation'],
    queryFn: () => api.dbSaturation(),
    // Sunucu TTL'i 30 sn — altına inmek sıcak slotu ıskalatır.
    staleTime: 30_000,
  });

  const data = q.data as DBSaturation | undefined;
  const rows = useMemo(() => data?.rows ?? [], [data]);
  const worst = useMemo(() => worstSaturation(rows), [rows]);

  // KARO HİÇ KURULMAZ: yükleniyorken de, hata varken de, veri yokken de.
  // Yükleme iskeleti bile çizmiyoruz — çünkü karonun ÇIKACAĞI belli
  // değil ve bir saniye görünüp kaybolan bir karo, şeridin kaç karo
  // olduğunu her yüklemede değiştirirdi.
  if (!worst) return null;

  const tone = saturationTone(worst.pct);
  const accent = tone === 'err' ? 'var(--err)' : tone === 'warn' ? 'var(--warn)' : 'var(--ok)';
  const lookbackMin = Math.round((data?.lookbackSeconds ?? 600) / 60);
  const others = rows.length - 1;

  return (
    <div className="card ov-kpi"
      role="button" tabIndex={0}
      onClick={() => onFocusInstance(worst.system, worst.instance)}
      onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') onFocusInstance(worst.system, worst.instance); }}
      style={{ cursor: 'pointer' }}
      title={
        `EN DAR HAVUZ: ${saturationLabel(worst)} — ${Math.round(worst.usage).toLocaleString()} / ` +
        `${Math.round(worst.limit).toLocaleString()} (%${worst.pct.toFixed(1)}).\n` +
        `Bu bir GAUGE: son ${lookbackMin} dakikanın EN SON değeri, sayfa aralığının ortalaması DEĞİL ` +
        `(24 saate ortalanmış bir doygunluk tam da görülmesi gereken zirveyi saklardı).\n` +
        (others > 0 ? `Pencerede ölçülen ${rows.length} havuzdan en darı; diğer ${others} tanesi daha boş.\n` : '') +
        'Tıklayın: bu instance\'ın receiver paneli açılır (oturumlar, wait class\'ları, tablespace).'
      }>
      <div className="ov-kpi-accent" style={{ background: accent }} />
      <div className="ov-lab">Havuz doygunluğu</div>
      <div className="ov-val">
        {worst.pct.toFixed(worst.pct < 10 ? 1 : 0)}<span className="ov-unit">%</span>
      </div>
      <div className="ov-delta" style={{
        color: tone === 'err' ? 'var(--err)' : tone === 'warn' ? 'var(--warn)' : 'var(--text3)',
        fontWeight: 600, maxWidth: '100%', overflow: 'hidden',
        textOverflow: 'ellipsis', whiteSpace: 'nowrap',
      }}>
        {saturationLabel(worst)}
      </div>
    </div>
  );
}
