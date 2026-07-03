import { useEffect, useState } from 'react';
import { getRaw, setRaw } from '@/lib/storage';

// Density toggle — global UI density level. Datadog has 2 levels
// (compact/comfortable), Grafana has 3, Salesforce has 4. The
// "barely visible difference" feedback came from a 2-level
// implementation; this version exposes 4 steps so the
// difference between extremes is meaningful while a casual user
// can step one at a time.
//
// Mechanism: a `data-density` attribute on <html> drives every
// CSS overlay in globals.css via `[data-density="dense"] .row {
// font-size: 11px }`-style selectors. localStorage persists the
// choice across reloads.

type Density = 'spacious' | 'comfortable' | 'compact' | 'dense';
const STORAGE_KEY = 'coremetry-density';

// Step order — clicking the toggle cycles through these. We go
// up (denser → smaller) since the typical reason an operator
// touches the toggle is "I want more on screen"; rolling back to
// spacious is one extra click. Cycles back to spacious after
// dense.
const STEPS: Density[] = ['spacious', 'comfortable', 'compact', 'dense'];
const LABEL: Record<Density, string> = {
  spacious:    'Spacious',
  comfortable: 'Comfortable',
  compact:     'Compact',
  dense:       'Dense',
};
// Each step's glyph approximates the line density — sparser
// bars on the left, packed bars on the right.
const GLYPH: Record<Density, string> = {
  spacious:    '☷',  // wide gaps
  comfortable: '≡',  // 3 lines
  compact:     '☰',  // 3 close lines
  dense:       '▤',  // packed rows
};

export function DensityToggle() {
  const [density, setDensity] = useState<Density>('comfortable');

  useEffect(() => {
    const stored = getRaw(STORAGE_KEY) as Density | null;
    // Tolerate legacy 2-level values from pre-v0.4.85 by mapping
    // them onto the new scale.
    const initial: Density = (() => {
      if (stored && (STEPS as string[]).includes(stored)) return stored as Density;
      return 'comfortable';
    })();
    setDensity(initial);
    document.documentElement.setAttribute('data-density', initial);
  }, []);

  const cycle = () => {
    const i = STEPS.indexOf(density);
    const next = STEPS[(i + 1) % STEPS.length];
    setDensity(next);
    document.documentElement.setAttribute('data-density', next);
    setRaw(STORAGE_KEY, next);
  };

  const tip = `Density: ${LABEL[density]} — click to cycle`;
  return (
    <button className="theme-toggle" onClick={cycle}
      aria-label={tip}
      title={tip}
      style={{ fontSize: 14, lineHeight: 1 }}>
      {GLYPH[density]}
    </button>
  );
}
