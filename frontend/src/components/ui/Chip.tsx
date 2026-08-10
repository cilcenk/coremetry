import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react';

// Chip — the bordered, labelled affordance (v0.9.894 dalgası, mB2 + MB6).
//
// Depoda 10+ site bunu dosya başına elle kuruyordu ve üçü aynı accent
// tint formülünü satır-içi `color-mix` ile YENİDEN TÜRETİYORDU
// (`--accent-bg` / `--accent-border` token'ları dururken). Yan etkisi
// yalnız tekrar değil: `all: unset` kurulumları `:focus-visible`
// halkasını da siliyordu, yani klavyeyle gezen operatör bu çiplerin
// üzerinden geçerken hiçbir şey görmüyordu — IconButton'ınkiyle aynı
// sessiz a11y kaybı.
//
// ── RADIUS: operatör kararı 2026-08-10 ────────────────────────────────
// Çip radius'u depoda DÖRT değerdi: 999 · 14 · 8 · 3. Karar: İKİ değere
// iner — `pill` (999; filtre/etiket çipleri) ve varsayılan
// `var(--radius-sm)` (köşeli rozetler). 14/8/3 sapmaları bu ikisine
// normalize edilir; ortaya çıkan küçük görsel fark BİLİNÇLİDİR. Üçüncü
// bir rung açmak sapmayı "kural" hâline getirir, ki bu ailenin varlık
// sebebi tam olarak oydu.
//
// ── SINIF ADI: `.chip` KULLANILAMAZ ───────────────────────────────────
// `.chip` bu depoda ZATEN TANIMLI ve CANLI (globals.css, radius 20px,
// bg0, statik `key/value` meta rozeti) — ProblemDetail ve Incident
// sayfalarında sekiz tüketicisi var. Atomun tabanına o adı verseydik iki
// detay sayfasının meta satırı sessizce yeniden boyanırdı. Dikkat:
// `primitiveClasses` kapısı bunu YAKALAYAMAZDI, çünkü sınıf CSS'te
// TANIMLI — sadece BAŞKA bir şey olarak. Bu yüzden aynı commit'te kapıya
// "sahiplenilmemiş çakışma" iddiası eklendi.
//
// Taban `btn-chip`, değiştiriciler `ch-*`. IconButton'ın `ib-*` dersinin
// aynısı: paylaşılan kısa adlar (`sm`, `accent`) element-seviyesi
// `button.sm { padding: 3px 9px }` kuralıyla (özgüllük 0,1,1) çakışırdı.
//
// ── onRemove: `<button>` İÇİNE `<button>` KOYULAMAZ ───────────────────
// `onRemove` verildiğinde kök bir `<span>` sarmalayıcıya döner; içeride
// etiket + × ayrı düğmeler olur. Etiket YALNIZCA `onClick` varsa
// `<button>`dur — aksi hâlde düz `<span>`. Tıklanamayan bir şeyi buton
// gibi göstermek sahte affordance olurdu (Inbox/Traces çipleri statik).

type Tone = 'neutral' | 'accent';
type Size = 'xs' | 'sm';

export interface ChipProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  /** Seçili/filtreleniyor durumu — `aria-pressed` de basar. */
  active?: boolean;
  tone?: Tone;
  size?: Size;
  /** true → borderRadius 999. false/atlanmış → `var(--radius-sm)`. */
  pill?: boolean;
  /** Verilirse çip sarmalayıcıya döner + içselleştirilmiş × düğmesi. */
  onRemove?: () => void;
  /** ×'in erişilebilir adı — glif tek başına ad taşımaz. */
  removeLabel?: string;
  children?: ReactNode;
}

const toneClass: Record<Tone, string> = {
  neutral: '',
  accent:  'ch-accent',
};
const sizeClass: Record<Size, string> = {
  xs: 'ch-xs',
  sm: 'ch-sm',
};

export const Chip = forwardRef<HTMLButtonElement, ChipProps>(function Chip(
  { active, tone = 'neutral', size = 'sm', pill, onRemove, removeLabel = 'Remove',
    className, children, type = 'button', ...rest },
  ref,
) {
  const classes = [
    'btn-chip',
    toneClass[tone],
    sizeClass[size],
    pill ? 'ch-pill' : '',
    active ? 'active' : '',
    className,
  ].filter(Boolean).join(' ');

  const pressed = active === undefined ? undefined : active;

  if (!onRemove) {
    return (
      <button ref={ref} type={type} className={classes} aria-pressed={pressed} {...rest}>
        {children}
      </button>
    );
  }

  // Sarmalayıcı hâli. `ch-static` sarmalayıcının imlecini `default`a
  // çeker: kutunun kendisi tıklanabilir DEĞİL, içindeki düğmeler öyle.
  const interactive = typeof rest.onClick === 'function';
  return (
    <span className={`${classes} ch-static`} title={interactive ? undefined : rest.title}>
      {interactive
        ? <button ref={ref} type={type} className="btn-chip-label" aria-pressed={pressed} {...rest}>
            {children}
          </button>
        : <span className="btn-chip-label">{children}</span>}
      <button type="button" className="btn-chip-x" aria-label={removeLabel}
        onClick={e => { e.stopPropagation(); onRemove(); }}>
        <span aria-hidden="true">×</span>
      </button>
    </span>
  );
});
