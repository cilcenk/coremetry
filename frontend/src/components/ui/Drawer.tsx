import { useEffect, useRef } from 'react';
import { useEscLayer } from '@/lib/escLayer';
import { createPortal } from 'react-dom';
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
  const panelRef = useRef<HTMLDivElement>(null);
  const lastFocusRef = useRef<Element | null>(null);

  // v0.9.950 (E2/Ö28) — Esc artık KATMAN. Öncesinde kendi document
  // dinleyicisiydi ve üstüne bir ⌘K açıldığında ikisi birden ateşliyordu:
  // paleti Esc'le kapatan operatör çekmeceyi de kaybediyordu. Yığın LIFO,
  // yani "en son açılan en üstte".
  useEscLayer(true, onClose);

  // mK1 (v0.9.927) — odak taşıma + kaydırma kilidi YALNIZ perde kipinde.
  //
  // Bu şartın kaldırılması dalganın tek gerçek regresyon riskiydi:
  // `backdrop={false}` CoSRE sohbetidir ve varlık gerekçesi açıkken
  // sayfayla ÇALIŞILABİLMESİ (operatör tabloyu kaydırıyor, başka bir
  // trace açıyor, sonra sorusunu yazıyor). Odağı sohbete hapsetmek ve
  // `body{overflow:hidden}` basmak o gerekçeyi BİREBİR tersine çevirirdi
  // — sohbet, eşlik ettiği işi imkânsız kılan bir modale dönüşürdü.
  //
  // Perde kipinde ise çekmece zaten modaldir (arkası karartılmış ve
  // tıklanamaz), dolayısıyla odağın içeri gelmesi ve sayfanın
  // kaydırılmaması BEKLENEN davranıştır.
  useEffect(() => {
    if (!backdrop) return;
    lastFocusRef.current = document.activeElement;
    const t = setTimeout(() => {
      const root = panelRef.current;
      if (!root) return;
      const first = root.querySelector<HTMLElement>(
        'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])');
      (first ?? root).focus({ preventScroll: true });
    }, 0);
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    return () => {
      clearTimeout(t);
      document.body.style.overflow = prevOverflow;
      if (lastFocusRef.current instanceof HTMLElement) {
        lastFocusRef.current.focus({ preventScroll: true });
      }
    };
  }, [backdrop]);

  // Portal: çekmece `#main`in DOM ağacından çıkıp `document.body`ye
  // taşınıyor. Görsel çıktı birebir aynı — panel zaten `position:fixed`
  // ve depoda onu hapsedecek bir yığınlama bağlamı (ata elemanda
  // transform/filter/contain) ÖLÇÜLDÜ, yok. Kazanç: çekmece artık
  // çağıranın `overflow`/`contain` kurallarının insafına kalmıyor.
  //
  // Portal'ın z beraberliğini DOM-sırası kumarına çevirme riski
  // v0.9.911'de kapandı: Drawer'ın irtifası artık kipe bağlı ayrı bir
  // rung ve PageLoader ile olan 30 beraberliği `--z-app-splash`e taşındı.
  return createPortal(
    <>
      {backdrop && (
        <div onClick={onClose}
          style={{
            position: 'fixed', inset: 0, background: 'var(--backdrop)',
            zIndex: 'var(--z-drawer)', animation: 'fadeIn 120ms ease-out',
          }} />
      )}
      {/* v0.9.988 (D6.3) — `drawer-panel` sınıfı DAR EKRAN kancası.
          Genişlik zaten `min(width, 100vw)` olduğu için telefonda çekmece
          fiilen tam ekran; eksik olan iç ölçülerdi. `var(--sp-7)` dolgu
          390px'lik bir ekranda içeriğe 60px'ten fazla yiyordu ve kapatma
          butonu 24px'lik bir dokunma hedefiydi (WCAG 2.5.8 tabanının
          altında). Sınıfın MASAÜSTÜ kuralı YOK — yalnız `@media
          (max-width: 640px)` bloğunda tanımlı, dolayısıyla geniş ekranda
          tek piksel değişmiyor. */}
      <div ref={panelRef} tabIndex={-1} className="drawer-panel" style={{
        position: 'fixed', right: 0, top: 0, bottom: 0,
        width: `min(${width}px, 100vw)`,
        background: 'var(--bg)', borderLeft: '1px solid var(--border)',
        boxShadow: 'var(--shadow-pop)',
        // MK2 (v0.9.911) — irtifa KİPE bağlı, sabit değil.
        //   backdrop=true  → --z-drawer-panel: çekmece bir özneyi
        //     İNCELİYOR, sayfanın dropdown'larının üstünde olmalı.
        //   backdrop=false → --z-nav: CoSRE sohbeti sayfaya EŞLİK
        //     ediyor. Sohbeti drawer rungına çıkarmak, sohbet açıkken
        //     topbar'ın TimeRangePicker panelini sohbetin ALTINDA
        //     bırakırdı — yani sohbetin varlık gerekçesini (açıkken
        //     sayfayla çalışabilmek) tersine çevirirdi.
        zIndex: backdrop ? 'var(--z-drawer-panel)' : 'var(--z-nav)',
        // DOLGU ARTIK SATIR İÇİNDE DEĞİL (v0.9.988, D6.3). Satır içi
        // stil bir `@media` kuralını HER ZAMAN yener; `padding:
        // var(--sp-7)` burada kalsaydı dar ekran kuralı sessizce ölürdü —
        // `gridCollapse.test.ts`in çividiği hata sınıfının aynısı.
        // Masaüstü değeri `.drawer-panel` taban kuralında, aynı token.
        // Overlay'siz kipte gövdeyi çağıran düzenliyor; modal kipte
        // bugünkü davranış (tek dikey kaydırma) aynen sürüyor.
        ...(bodyStyle ?? { overflowY: 'auto' }),
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--sp-4)', marginBottom: 'var(--sp-2)' }}>
          {header}
          <span style={{ flex: 1 }} />
          <button className="sec drawer-close" onClick={onClose} aria-label="Close"
            style={{ padding: 'var(--sp-2) var(--sp-3)', display: 'inline-flex' }}>
            <X size={14} />
          </button>
        </div>
        {children}
      </div>
    </>,
    document.body,
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
        fontSize: 'var(--fs-2xs)', color: 'var(--text3)', textTransform: 'uppercase',
        letterSpacing: 0.5, marginBottom: 'var(--sp-3)', fontWeight: 600,
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
    <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--sp-5)' }}>
      <span style={{ fontSize: 'var(--fs-xs)', color: 'var(--text2)', width: 60 }}>{label}</span>
      <span
        onClick={onClick}
        style={onClick ? { cursor: 'pointer' } : undefined}
        title={onClick ? `${label} — click to expand` : undefined}>
        <Sparkline values={values} width={420} height={34} color={color} title={label} />
      </span>
    </div>
  );
}
