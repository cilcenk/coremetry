import type { ReactNode } from 'react';
import { TimeRangePicker } from './TimeRangePicker';
import { EnvPicker } from './EnvPicker';
import { LangToggle } from './LangToggle';
import { DensityToggle } from './DensityToggle';
import { ThemeToggle } from './ThemeToggle';
import { TopbarSearch } from './TopbarSearch';
import type { TimeRange } from '@/lib/types';
import { ContextBar, type ContextBarProps } from './ContextBar';

// `range` is optional — pages that aren't time-bound (e.g. /users) omit it
// and the time picker is hidden. The global env picker (v0.8.383) rides the
// same gate: an env filter only means something on telemetry-bound pages.
//
// v0.8.516 (perf raporu #22) — showEnv: Inbox/Problems gibi range'siz
// AMA env-filtreli sayfalar picker'ı ayrıca isteyebilir. Eskiden env
// bu sayfalarda UYGULANIYOR ama DEĞİŞTİRİLEMİYORDU — başka sayfada
// seçilen 'uat' inbox'ı sessizce daraltıp kalıyordu.
//
// v0.9.864 (UX denetimi §4.3 seçenek (b), operatör onayı 2026-08-09) —
// envApplies: bu sayfa env filtresini GERÇEKTEN uyguluyor mu? Varsayılan
// false; picker uygulamayan sayfada devre dışı + dürüst ipuçlu görünür.
// Gerekçe ve neden gizlemek yerine devre dışı: EnvPicker.tsx.
export function Topbar({ title, range, onRangeChange, showEnv, envApplies, context, actions }: {
  title: string;
  /** v0.10.354 (operatör: Trace eylemleri "sağ üstte gri alana") — sayfa
   *  eylemleri; arama kutusu ile env/zaman seçicisi ARASINDA. Zaman seçici
   *  en sağda kalır (v0.10.255 kuralı). */
  actions?: ReactNode;
  range?: TimeRange;
  onRangeChange?: (r: TimeRange) => void;
  showEnv?: boolean;
  /** Sayfa `?env=` değerini kendi sorgularına GEÇİRİYORSA true. */
  envApplies?: boolean;
  /** v0.10.250 — ContextBar: verilirse EnvPicker+TimeRangePicker çiftinin yerine geçer (aynı yuva). */
  context?: ContextBarProps;
}) {
  return (
    <div id="topbar">
      <h1>{title}</h1>
      {/* v0.9.1019 (G1) — global aramanın GÖRÜNÜR kapısı.
          Öncelik sırası dar topbar'da şu: başlık ezilir (`h1` zaten tek
          esneyebilen eleman, D2.6), sonra arama kutusunun kısayol
          ipuçları düşer, sonra yer tutucu metni kısalır. Range/env/live
          hiç dokunulmuyor — onlar sayfanın SORGUSUNU değiştiriyor,
          arama yalnız gezinme. Bir operatörün yanlışlıkla pencereyi
          daraltması, arama kutusunu kaybetmesinden pahalı. */}
      <TopbarSearch />
      {actions && <div className="topbar-actions">{actions}</div>}
      {context ? <ContextBar {...context} /> : (
        <>
          {showEnv && !(range && onRangeChange) && <EnvPicker applies={envApplies} />}
          {range && onRangeChange && (
            <>
              <EnvPicker applies={envApplies} />
              <TimeRangePicker value={range} onChange={onRangeChange} />
            </>
          )}
        </>
      )}
      {(range || context) && <div className="topbar-prefs-sep" />}
      <div className="topbar-prefs">
        <LangToggle />
        <DensityToggle />
        <ThemeToggle />
      </div>
    </div>
  );
}
