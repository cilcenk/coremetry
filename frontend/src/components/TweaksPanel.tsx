import { useEffect, useState } from 'react';

// TweaksPanel (v0.7.117) — the design handoff's design-time tweak controls,
// brought into the product as a floating ⚙ panel: dark mode, accent colour,
// and table density. Each control drives the SAME mechanism the existing
// toggles use — data-theme / data-density on <html> + the --accent override
// — and persists to the same localStorage keys, so it stays in sync with the
// Topbar's ThemeToggle / DensityToggle.

const THEME_KEY = 'coremetry-theme';
const DENSITY_KEY = 'coremetry-density';
const ACCENT_KEY = 'coremetry-accent';

type Theme = 'dark' | 'light';
type Density = 'spacious' | 'comfortable' | 'compact' | 'dense';
const DENSITIES: Density[] = ['spacious', 'comfortable', 'compact', 'dense'];
const ACCENTS = [
  { label: 'Blue', v: '#0969da' }, { label: 'Purple', v: '#8250df' },
  { label: 'Teal', v: '#137775' }, { label: 'Orange', v: '#bc4c00' }, { label: 'Brand', v: '#e30613' },
];

export function TweaksPanel() {
  const [open, setOpen] = useState(false);
  const [theme, setTheme] = useState<Theme>('light');
  const [density, setDensity] = useState<Density>('comfortable');
  const [accent, setAccent] = useState('');

  // Hydrate from what the boot script / branding already applied to <html>.
  useEffect(() => {
    const r = document.documentElement;
    setTheme((r.getAttribute('data-theme') as Theme) ?? 'light');
    setDensity((r.getAttribute('data-density') as Density) ?? 'comfortable');
    let a = '';
    try { a = localStorage.getItem(ACCENT_KEY) ?? ''; } catch { /* ignore */ }
    setAccent(a);
    if (a) { r.style.setProperty('--accent', a); r.style.setProperty('--accent2', a); }
  }, []);

  const applyTheme = (t: Theme) => {
    setTheme(t);
    document.documentElement.setAttribute('data-theme', t);
    try { localStorage.setItem(THEME_KEY, t); } catch { /* ignore */ }
  };
  const applyDensity = (d: Density) => {
    setDensity(d);
    document.documentElement.setAttribute('data-density', d);
    try { localStorage.setItem(DENSITY_KEY, d); } catch { /* ignore */ }
  };
  const applyAccent = (v: string) => {
    setAccent(v);
    const r = document.documentElement;
    if (v) { r.style.setProperty('--accent', v); r.style.setProperty('--accent2', v); }
    else { r.style.removeProperty('--accent'); r.style.removeProperty('--accent2'); }
    try { v ? localStorage.setItem(ACCENT_KEY, v) : localStorage.removeItem(ACCENT_KEY); } catch { /* ignore */ }
  };

  return (
    <>
      <button className="tweaks-fab" title="Appearance tweaks" aria-label="Appearance tweaks"
        onClick={() => setOpen(o => !o)}>⚙</button>
      {open && (
        <div className="tweaks-panel" role="dialog" aria-label="Appearance tweaks">
          <div className="tweaks-h">
            <h3>Tweaks</h3>
            <button className="tweaks-x" aria-label="Close" onClick={() => setOpen(false)}>×</button>
          </div>
          <div className="tweaks-sec">Appearance</div>
          <label className="tweaks-row">
            <span>Dark mode</span>
            <input type="checkbox" checked={theme === 'dark'} onChange={e => applyTheme(e.target.checked ? 'dark' : 'light')} />
          </label>
          <div className="tweaks-row">
            <span>Accent</span>
            <div className="tweaks-swatches">
              {ACCENTS.map(a => (
                <button key={a.v} className={'tweaks-sw' + (accent === a.v ? ' on' : '')}
                  style={{ background: a.v }} title={a.label} aria-label={a.label} onClick={() => applyAccent(a.v)} />
              ))}
              <button className={'tweaks-sw tweaks-sw-reset' + (accent === '' ? ' on' : '')}
                title="Default" aria-label="Default accent" onClick={() => applyAccent('')}>↺</button>
            </div>
          </div>
          <div className="tweaks-sec">Density</div>
          <div className="tweaks-row">
            <div className="segmented tweaks-density">
              {DENSITIES.map(d => (
                <button key={d} className={density === d ? 'active' : ''} title={d} onClick={() => applyDensity(d)}>
                  {d.charAt(0).toUpperCase() + d.slice(1)}
                </button>
              ))}
            </div>
          </div>
        </div>
      )}
    </>
  );
}
