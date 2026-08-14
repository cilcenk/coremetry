import { forwardRef, type InputHTMLAttributes } from 'react';
import { IconSearch } from '@/components/icons';

// SearchField (v0.9.1012, etkileşim denetimi M8 / KN-5) — ARAMA bir tür,
// "bir input" değil.
//
// KÖK NEDEN: bu depoda arama kutusunun tür tanımı yoktu, dolayısıyla her
// özelliği çağırana kalmıştı ve hepsi ölçülünce dağıldı:
//   · büyüteç 39 kutunun 2'sinde
//   · kısayol ipucu 39'un 0'ında (depoda `<kbd>` yalnız ShortcutsHelp'te,
//     yani yalnız kısayolu ZATEN bilenin gördüğü yerde)
//   · `type="search"` 39'un 1'inde (mobil klavyede "Ara" tuşu + native ✕
//     kaybı)
//   · placeholder kalitesi çağırana bağlı
//
// SOMUT BEDELİ: `Services.tsx`te "Filter services…" (arama) ile
// "Min spans" (sayı eşiği) ekranda BİREBİR aynı görünüyordu. K1'in ruhu —
// arama alanı bardaki diğer alanlardan görsel olarak ayrışmalı —
// sağlanmıyordu.
//
// Bu atom `.trace-lookup` CSS'inin ve `KqlSearchInput`un gömülü svg'sinin
// genelleştirilmiş hâli: ikisi de aynı problemi tek kullanımlık çözmüştü.
//
// DÜZEN DEĞİŞMİYOR: ikon ve rozet kutunun İÇİNDE yaşıyor (`padding-left`
// / `padding-right`), kutu genişlikleri korunuyor. Denetimin kendi
// ayrımıyla bu "görünen kabuk" değil mekanik bir dalga.
export interface SearchFieldProps
  extends Omit<InputHTMLAttributes<HTMLInputElement>, 'type' | 'value' | 'onChange'> {
  value: string;
  onChange: (v: string) => void;
  /**
   * Sağ iç kenardaki soluk kısayol rozeti (`/`, `⌘K`). Odaklanınca
   * kayboluyor — ipucu, kutuyu KULLANIRKEN değil BULURKEN gerekli.
   */
  hint?: string;
  /** Kutu genişliği; sayı px sayılır. */
  width?: number | string;
}

export const SearchField = forwardRef<HTMLInputElement, SearchFieldProps>(
  function SearchField({ value, onChange, hint, width, className, style, ...rest }, ref) {
    return (
      <span className="sf-wrap" style={{ width, ...style }}>
        {/* Dekoratif: tıklama inputa düşsün diye `pointer-events: none`
            (CSS'te). Bir arama ikonuna tıklayan operatör kutuya odak
            bekler, hiçbir şey olmamasını değil. */}
        <IconSearch size={13} className="sf-icon" />
        <input
          {...rest}
          ref={ref}
          // v0.9.1012 (L2) — `type="search"` depoda TEK bir inputta
          // vardı. Mobil klavyede Enter'ı "Ara" yapıyor ve native temizle
          // affordance'ını getiriyor; ikisi de bedava.
          type="search"
          value={value}
          onChange={e => onChange(e.target.value)}
          className={['sf-input', className].filter(Boolean).join(' ')}
        />
        {hint && <kbd className="sf-hint">{hint}</kbd>}
        {value !== '' && (
          <button type="button" className="sf-clear" aria-label="Clear search"
            onClick={() => onChange('')}>
            <span aria-hidden="true">✕</span>
          </button>
        )}
      </span>
    );
  },
);
