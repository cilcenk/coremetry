import { TimeRangePicker } from './TimeRangePicker';
import { EnvPicker } from './EnvPicker';
import { LangToggle } from './LangToggle';
import { DensityToggle } from './DensityToggle';
import { ThemeToggle } from './ThemeToggle';
import { LiveTicker } from './LiveTicker';
import type { TimeRange } from '@/lib/types';

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
export function Topbar({ title, range, onRangeChange, showEnv, envApplies }: {
  title: string;
  range?: TimeRange;
  onRangeChange?: (r: TimeRange) => void;
  showEnv?: boolean;
  /** Sayfa `?env=` değerini kendi sorgularına GEÇİRİYORSA true. */
  envApplies?: boolean;
}) {
  return (
    <div id="topbar">
      <h1>{title}</h1>
      {showEnv && !(range && onRangeChange) && <EnvPicker applies={envApplies} />}
      {range && onRangeChange && (
        <>
          <EnvPicker applies={envApplies} />
          <TimeRangePicker value={range} onChange={onRangeChange} />
        </>
      )}
      {range && <div className="topbar-prefs-sep" />}
      {/* v0.5.280 — live ingest ticker. Visceral feedback that
          spans/logs/metrics are actually flowing; mounts once
          via Topbar so every page carries it. Hidden until the
          second sample lands so we don't show a misleading
          "0 sp/s" on first paint. */}
      <LiveTicker />
      <div className="topbar-prefs">
        <LangToggle />
        <DensityToggle />
        <ThemeToggle />
      </div>
    </div>
  );
}
