import { useNavigate } from 'react-router-dom';
import { useShortcuts } from '@/lib/keyboard';

// GlobalShortcuts (v0.5.444) — registers app-wide power-user
// shortcuts: '/' to focus the page search input, and 'g <x>'
// two-key sequences for fast page navigation (Datadog / Linear
// muscle memory). Self-contained, mounted once at AppShell so
// every authenticated page inherits the bindings without
// per-page imports. Labels surface in the '?' help modal via
// the existing listShortcuts() registry walk.

const PAGES: { key: string; label: string; to: string }[] = [
  { key: 'g h', label: 'Go to Home',       to: '/' },
  { key: 'g s', label: 'Go to Services',   to: '/services' },
  { key: 'g t', label: 'Go to Traces',     to: '/traces' },
  { key: 'g l', label: 'Go to Logs',       to: '/logs' },
  { key: 'g m', label: 'Go to Metrics',    to: '/metrics' },
  { key: 'g p', label: 'Go to Problems',   to: '/problems' },
  { key: 'g d', label: 'Go to Dashboards', to: '/dashboards' },
  { key: 'g a', label: 'Go to Anomalies',  to: '/anomalies' },
  { key: 'g e', label: 'Go to Explore',    to: '/explore' },
  { key: 'g o', label: 'Go to Topology',   to: '/topology' },
  { key: 'g i', label: 'Go to Incidents',  to: '/incidents' },
];

// focusPageSearch finds the first visible, enabled text input
// on the page and focuses+selects it. Works for the common
// "search/filter is the first field" layout on /traces, /logs,
// /services, /anomalies etc. without per-page markup. If the
// first input isn't the right one on some page, the operator
// can Tab from there.
function focusPageSearch(): void {
  const inputs = document.querySelectorAll<HTMLInputElement>(
    'input[type="text"], input[type="search"], input:not([type])'
  );
  for (const el of Array.from(inputs)) {
    // offsetParent === null catches `display:none` and detached
    // nodes; disabled inputs shouldn't grab focus either.
    if (el.offsetParent !== null && !el.disabled && !el.readOnly) {
      el.focus();
      el.select();
      return;
    }
  }
}

export function GlobalShortcuts() {
  const navigate = useNavigate();

  useShortcuts(
    [
      {
        keys: '/',
        label: 'Focus page search',
        group: 'Navigation',
        // keyboard.ts already calls preventDefault before the
        // handler, so the browser's built-in quick-find on '/'
        // is suppressed automatically.
        handler: () => focusPageSearch(),
      },
      ...PAGES.map(p => ({
        keys: p.key,
        label: p.label,
        group: 'Navigation',
        handler: () => navigate(p.to),
      })),
    ],
    // navigate identity is stable across renders, so an empty
    // dep list registers once and lives for the shell lifetime.
    [],
  );

  return null;
}
