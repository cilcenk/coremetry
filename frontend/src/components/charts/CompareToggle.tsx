// CompareToggle.tsx — v0.10.315 (chart-layer Dilim 2.3b): "Compare to:"
// düğme sırası. v0.8.x'ten beri ServiceCharts içindeydi; Pod da aynı
// seçimi (?compare=) sunduğu için ortak bileşene çıktı. Dört mod, tek
// sözlük (lib/chart/compareParam.ts); seçim ÇAĞIRANIN (useCompareParam).
import { Button } from '@/components/ui/Button';
import type { CompareMode } from '@/lib/chart/compareParam';

const MODES: CompareMode[] = ['off', '24h', '7d', 'prev'];

export function CompareToggle({ value, onChange }: { value: CompareMode; onChange: (m: CompareMode) => void }) {
  return (
    <>
      <span style={{ textTransform: 'uppercase', letterSpacing: 0.4, fontWeight: 700 }}>Compare to:</span>
      {MODES.map(m => (
        <Button key={m} size="xs"
          variant={value === m ? 'accent' : 'secondary'}
          aria-pressed={value === m}
          onClick={() => onChange(m)}
          title={m === 'off' ? 'No comparison'
            : m === 'prev' ? 'Previous window of the same length'
            : `${m} ago at the same time`}
          style={{ fontFamily: 'ui-monospace, SFMono-Regular, monospace' }}>
          {m === 'off' ? 'off' : m === 'prev' ? 'prev window' : m}
        </Button>
      ))}
    </>
  );
}
