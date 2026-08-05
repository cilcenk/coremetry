import { useEffect } from 'react';
import { X } from 'lucide-react';
import { Sparkline } from '@/components/Sparkline';

// Drawer — sağ-kenar çekmece kabuğunun TEK evi (v0.8.465,
// sadeleştirme #11). Overlay + slide-in panel + Esc/✕/overlay kapatma
// + başlık satırı tek yerde; "one design language" kuralının Modal'da
// olup Drawer'da eksik kalan ayağı. İçerik (bölümler, tablolar,
// trendler) çağıranda kalır. İlk migrasyon External + Hosts (bayt-bayt
// aynı kopyalar); kalan 5 çekmece yüzeyi kendi sürümlerinde taşınır.
export function Drawer({ onClose, header, width = 560, backdrop = true, bodyStyle, children }: {
  onClose: () => void;
  // Başlık satırının sol tarafı (ad + rozetler); ✕ butonunu kabuk koyar.
  header: React.ReactNode;
  width?: number;
  // backdrop (v0.9.654) — arkadaki sayfayı KARARTIP tıklanamaz yapan
  // katman. Varsayılan true: bir özneyi (host, endpoint, exception)
  // İNCELEYEN çekmece modaldır, arkasıyla iş yapılmaz.
  //
  // false → CoSRE sohbeti. Operatör sohbet açıkken tabloyu kaydırıyor,
  // başka bir trace açıyor, sonra sorusunu yazıyor; overlay bunu
  // imkânsız kılardı. Sohbet bir özneyi incelemiyor, ona EŞLİK ediyor.
  //
  // Kapatma da buna bağlı: overlay yoksa "dışarı tıkla kapat" da yok —
  // sayfayla çalışmak sohbeti kapatmamalı. Esc ve ✕ her iki kipte de
  // çalışıyor.
  backdrop?: boolean;
  // bodyStyle — gövde düzenini çağıran devralabilir (sohbet kendi
  // dikey flex'ini kuruyor: kaydıran tur listesi + sabit composer).
  bodyStyle?: React.CSSProperties;
  children: React.ReactNode;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  return (
    <>
      {backdrop && (
        <div onClick={onClose}
          style={{
            position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.35)',
            zIndex: 30, animation: 'fadeIn 120ms ease-out',
          }} />
      )}
      <div style={{
        position: 'fixed', right: 0, top: 0, bottom: 0,
        width: `min(${width}px, 100vw)`,
        background: 'var(--bg)', borderLeft: '1px solid var(--border)',
        boxShadow: '-4px 0 24px rgba(0,0,0,0.3)',
        zIndex: 31, padding: 16,
        // Overlay'siz kipte gövdeyi çağıran düzenliyor; modal kipte
        // bugünkü davranış (tek dikey kaydırma) aynen sürüyor.
        ...(bodyStyle ?? { overflowY: 'auto' }),
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
          {header}
          <span style={{ flex: 1 }} />
          <button className="sec" onClick={onClose} aria-label="Close"
            style={{ padding: '4px 6px', display: 'inline-flex' }}>
            <X size={14} />
          </button>
        </div>
        {children}
      </div>
    </>
  );
}

// DrawerSection — başlıklı içerik bölümü (uppercase mikro-başlık).
export function DrawerSection({ title, children }: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div style={{ marginBottom: 18 }}>
      <div style={{
        fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase',
        letterSpacing: 0.5, marginBottom: 6, fontWeight: 600,
      }}>{title}</div>
      {children}
    </div>
  );
}

// DrawerTrendRow — etiket + Sparkline satırı (çekmece trend blokları).
// v0.9.0 — opsiyonel onClick: çağıran tıkla-büyüt (expanded uPlot)
// davranışı ekleyebilir; zaman ekseni verisi (bucket'lar) çağıranda
// olduğundan büyük grafiğin KENDİSİ de çağıranda render edilir — bu
// satır yalnız affordance'ı taşır. onClick'siz çağıranlar (Hosts
// drawer'ı) birebir eski davranışta.
export function DrawerTrendRow({ label, values, color, onClick }: {
  label: string;
  values: number[];
  color: string;
  onClick?: () => void;
}) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
      <span style={{ fontSize: 11, color: 'var(--text2)', width: 60 }}>{label}</span>
      <span
        onClick={onClick}
        style={onClick ? { cursor: 'pointer' } : undefined}
        title={onClick ? `${label} — click to expand` : undefined}>
        <Sparkline values={values} width={420} height={34} color={color} title={label} />
      </span>
    </div>
  );
}
