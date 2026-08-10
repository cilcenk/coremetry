import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react';

// DisclosureButton — the "click this heading to open the thing under it"
// affordance (v0.9.898 dalgası, mB3).
//
// Altı site bunu elle kuruyordu ve ikisi kırıktı:
//   • `aria-expanded` ALTIDA BİRİNDE vardı. Kalan beşinde ekran okuyucu
//     bir başlığın açılıp kapandığını hiç duymuyordu — sadece "buton".
//   • Glif İKİ DİL konuşuyordu: ▶/▼ (ServiceNeighbors, DBQueriesPanel,
//     TimeSeriesPanel, StatsLegend) ↔ ▸/▾ (Sidebar, ServiceLatencyHeatmap).
//     İkisi aynı ekranda görünebiliyordu (servis detayında ısı haritası
//     ▸, hemen altındaki DB paneli ▶).
//
// ── OPERATÖR KARARI 2026-08-10 ────────────────────────────────────────
// TEK glif çifti: `▸` kapalı / `▾` açık. İKİ anatomi:
//
//   `row`     — satır-başı chevron toggle. Küçük glif + etiket, zemin ve
//               kenarlık yok, satır içinde akar. (Lejant başlıkları,
//               "Latency distribution", Sidebar nav grubu.)
//   `section` — bölüm-başlığı disclosure. Tam genişlik, 14px iç boşluk,
//               glif SABİT GENİŞLİKTE bir kolonda durur ki uzun/kısa
//               başlıklarda etiketler aynı x'te hizalansın. Açıkken alt
//               kenarlık gövdeden ayırır. (Kart başlıkları.)
//
// Üçüncü bir anatomi açmak "her başlık kendi kuralını yazar"a geri
// dönüştür — ailenin varlık sebebi tam olarak oydu.
//
// Alt kenarlık `[aria-expanded="true"]` seçicisinden geliyor, ayrı bir
// sınıftan değil: görsel durumun ARIA durumundan AYRILMASI mümkün
// olmasın. Elle kurulmuş sitelerde tam olarak o ayrışma vardı — kenarlık
// `open`'ı izliyordu, `aria-expanded` ise hiç yoktu.

type Anatomy = 'row' | 'section';

export interface DisclosureButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  /** Açık mı — hem `aria-expanded` hem glif yönü buradan. */
  expanded: boolean;
  anatomy?: Anatomy;
  children?: ReactNode;
}

const anatomyClass: Record<Anatomy, string> = {
  row:     'dsc-row',
  section: 'dsc-section',
};

export const DisclosureButton = forwardRef<HTMLButtonElement, DisclosureButtonProps>(
  function DisclosureButton(
    { expanded, anatomy = 'row', className, children, type = 'button', ...rest },
    ref,
  ) {
    const classes = [
      'btn-disclose',
      anatomyClass[anatomy],
      className,
    ].filter(Boolean).join(' ');

    return (
      <button ref={ref} type={type} className={classes} aria-expanded={expanded} {...rest}>
        <span className="dsc-glyph" aria-hidden="true">{expanded ? '▾' : '▸'}</span>
        {children}
      </button>
    );
  },
);
