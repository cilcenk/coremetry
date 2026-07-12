import { useNavigate } from 'react-router-dom';
import { useShortcuts } from '@/lib/keyboard';

// GlobalShortcuts (v0.5.444) — registers app-wide power-user
// shortcuts: '/' to focus the page search input, and 'g <x>'
// two-key sequences for fast page navigation (Datadog / Linear
// muscle memory). Self-contained, mounted once at AppShell so
// every authenticated page inherits the bindings without
// per-page imports. Labels surface in the '?' help modal via
// the existing listShortcuts() registry walk.

// v0.8.525 — SINGLE 'g <x>' registry. Öncesinde AppShell.tsx ayrı bir
// useShortcuts bloğu daha kaydediyordu; ikisi 'g o' (Topology vs
// Monitors) ve 'g m' (Metrics vs Service Map) üzerinde ÇAKIŞIYORdu ve
// yarısı ölü/gizli rotaya (g h → silinmiş Home, g o → gizli Monitors)
// gidiyordu. Artık tek yer; her satır CANLI + görünür bir rota, harf
// başına tek anlam. 'g n' → Inbox eklendi (günlük iniş yüzeyi).
const PAGES: { key: string; label: string; to: string }[] = [
  { key: 'g n', label: 'Go to Inbox',       to: '/inbox' },
  { key: 'g i', label: 'Go to Incidents',   to: '/incidents' },
  { key: 'g p', label: 'Go to Problems',    to: '/problems' },
  { key: 'g a', label: 'Go to Anomalies',   to: '/anomalies' },
  { key: 'g s', label: 'Go to Services',    to: '/services' },
  { key: 'g o', label: 'Go to Service Map', to: '/service-map' }, // 'o' = tOpology mnemonic (retired /topology → /service-map)
  { key: 'g t', label: 'Go to Traces',      to: '/traces' },
  { key: 'g m', label: 'Go to Metrics',     to: '/metrics' },
  { key: 'g l', label: 'Go to Logs',        to: '/logs' },
  { key: 'g e', label: 'Go to Explore',     to: '/explore' },
  { key: 'g r', label: 'Go to Runbooks',    to: '/runbooks' },
  { key: 'g d', label: 'Go to Dashboards',  to: '/dashboards' },
  { key: 'g x', label: 'Go to Alerts',      to: '/alerts' },
  { key: 'g c', label: 'Go to System',      to: '/system/stats' },
];

// focusPageSearch finds the page's search input and focuses+selects
// it. Resolution order:
//   1. The first element with `data-shortcut-search` — explicit
//      opt-in by the page so `/` lands where the operator expects
//      (e.g. the Service picker on /traces, not the trace-id
//      lookup which is the first DOM input).
//   2. The first visible text input or textarea on the page.
//      Fallback for pages that haven't been annotated yet.
// v0.5.454 — the fallback alone landed on the wrong field on
// /traces (Trace ID lookup is first in DOM, search picker is
// later) and missed /admin/sql entirely (textarea, not input).
function focusPageSearch(): void {
  const SELECT_VISIBLE = (el: HTMLElement) =>
    el.offsetParent !== null &&
    !(el as HTMLInputElement | HTMLTextAreaElement).disabled &&
    !(el as HTMLInputElement | HTMLTextAreaElement).readOnly;

  // 1. Explicit opt-in target wins.
  const opted = document.querySelectorAll<HTMLElement>('[data-shortcut-search]');
  for (const el of Array.from(opted)) {
    const target = el.matches('input, textarea')
      ? el
      : el.querySelector<HTMLElement>('input, textarea');
    if (target && SELECT_VISIBLE(target)) {
      target.focus();
      if ('select' in target && typeof (target as HTMLInputElement).select === 'function') {
        (target as HTMLInputElement).select();
      }
      return;
    }
  }

  // 2. Fallback — first visible text input or textarea.
  const inputs = document.querySelectorAll<HTMLElement>(
    'input[type="text"], input[type="search"], input:not([type]), textarea'
  );
  for (const el of Array.from(inputs)) {
    if (SELECT_VISIBLE(el)) {
      el.focus();
      if ('select' in el && typeof (el as HTMLInputElement).select === 'function') {
        (el as HTMLInputElement).select();
      }
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
