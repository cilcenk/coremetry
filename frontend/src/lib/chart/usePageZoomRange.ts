import { useCallback, useEffect, useRef } from 'react';
import { useUrlRange } from '@/lib/useUrlRange';
import { pushZoom, popZoom } from './zoomHistory';
import type { TimeRange } from '@/lib/types';

// usePageZoomRange (v0.9.429, grafik-audit Faz D / zoom-yığını
// birliği) — Grafana-parite M1 drag-zoom deseninin TEK sahibi.
// Service.tsx:71-99'daki referans implementasyon (v0.9.199) bugüne dek
// Dashboard/Pod/Endpoints'e ~17'şer satır ELLE kopyalanmıştı; drift
// kanıtlı (Endpoints fix'i sonradan sweep'le aldı, boş-yığın fallback
// preset'i sayfadan sayfaya kayıyordu). Davranış sözleşmesi:
//
//   - handleZoom(fromUnixSec, toUnixSec): brush öncesi görünüm yığına
//     itilir (pushZoom, MAX_DEPTH 32), pencere ?range=custom:<ms>-<ms>
//     olarak URL'e yazılır (useUrlRange, replace:true). Yığın EFEMER —
//     URL'e ASLA yazılmaz (copy-link/SavedViews sözleşmesi temiz).
//   - handleZoomReset (çift-tık): BİR ADIM geri (popZoom, LIFO). Yığın
//     boşken custom penceredeysek sayfanın default preset'ine dön
//     (derin linkle gelen custom dahil); preset'teyken no-op.
//   - Out-of-band range değişimi (Topbar preset seçimi, mutlak aralık)
//     yığını GEÇERSİZLEŞTİRİR (v0.9.199): bayat pre-zoom girdisi yeni
//     seçimin üstüne geri yazılamaz. zoomWroteRef yalnız zoom-sistemi
//     yazımlarını işaretler.
//   - onChange: zoom VE reset'in range'i gerçekten değiştirdiği her an
//     çağrılır — Logs'un resetPaging'i, Traces'in setPage(0)'ı gibi
//     sayfa-yerel yan etkiler buradan (v0.7.81 cursor kuralı).
//
// Birim sözleşmesi: handleZoom UNIX SANİYE alır (chart primitiflerinin
// onZoom'u — cursorOpts.selectRangeSec); ms/ns veren yüzeyler (TimeChart
// onBrush=ms, LogsHistogram=ns) çağrı yerinde böler. Explore BİLİNÇLİ
// dışarıda: zoom'u görsel/efemerdir, URL'e yazmaz (audit kriteri 6).
export function usePageZoomRange(defaultPreset: string, onChange?: () => void) {
  const [range, setRange] = useUrlRange(defaultPreset);
  const zoomStackRef = useRef<TimeRange[]>([]);
  const rangeRef = useRef(range); rangeRef.current = range;
  const zoomWroteRef = useRef(false);
  const onChangeRef = useRef(onChange); onChangeRef.current = onChange;

  useEffect(() => {
    if (zoomWroteRef.current) { zoomWroteRef.current = false; return; }
    zoomStackRef.current = [];
  }, [range.preset, range.fromMs, range.toMs]);

  const handleZoom = useCallback((fromUnixSec: number, toUnixSec: number) => {
    zoomStackRef.current = pushZoom(zoomStackRef.current, rangeRef.current);
    zoomWroteRef.current = true;
    setRange({
      preset: 'custom',
      fromMs: Math.round(fromUnixSec * 1000),
      toMs: Math.round(toUnixSec * 1000),
    });
    onChangeRef.current?.();
  }, [setRange]);

  const handleZoomReset = useCallback(() => {
    const { stack, view } = popZoom(zoomStackRef.current);
    zoomStackRef.current = stack;
    if (view) {
      zoomWroteRef.current = true;
      setRange(view);
      onChangeRef.current?.();
      return;
    }
    if (rangeRef.current.preset === 'custom') {
      zoomWroteRef.current = true;
      setRange({ preset: defaultPreset });
      onChangeRef.current?.();
    }
  }, [setRange, defaultPreset]);

  return { range, setRange, handleZoom, handleZoomReset };
}
