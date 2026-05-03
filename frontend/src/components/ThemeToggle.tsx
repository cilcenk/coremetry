'use client';
import { useEffect, useState } from 'react';

type Theme = 'dark' | 'light';

const STORAGE_KEY = 'qmetry-theme';

/**
 * Switches between dark and light palettes by toggling the
 * `data-theme` attribute on <html>. Persisted in localStorage; the
 * inline boot script in layout.tsx applies it pre-paint to avoid FOUC.
 */
export function ThemeToggle() {
  const [theme, setTheme] = useState<Theme>('dark');

  // Read the theme that the boot script already applied to <html>
  useEffect(() => {
    const t = (document.documentElement.getAttribute('data-theme') as Theme | null) ?? 'dark';
    setTheme(t);
  }, []);

  const toggle = () => {
    const next: Theme = theme === 'dark' ? 'light' : 'dark';
    setTheme(next);
    document.documentElement.setAttribute('data-theme', next);
    try { localStorage.setItem(STORAGE_KEY, next); } catch {}
  };

  return (
    <button className="theme-toggle" onClick={toggle}
      aria-label={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
      title={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}>
      {theme === 'dark' ? '☀' : '☾'}
    </button>
  );
}
