import { useEffect, useRef, useState } from 'react';
import { useEscLayer } from '@/lib/escLayer';
import type { ReactNode, KeyboardEvent } from 'react';
import { useNavigate } from 'react-router-dom';
import { CopyButton } from '@/components/CopyButton';
import { Button, IconButton, MenuItem } from '@/components/ui';
import { copyToClipboard } from '@/lib/clipboard';
import {
  describeMetricQuery,
  encodeMetricQuery,
  metricExploreHref,
  type MetricQuery,
} from '@/lib/metricQuery';
import type { PanelMenuAction } from '@/lib/chart/panelMenu';

// MetricPanel — "every metric is a doorway." The ONE reusable affordance every
// metric panel/KPI opts into so there are zero per-page click handlers: a panel
// hands its MetricQuery descriptor, the wrapper turns it into the deep-link
// menu (Explore / Edit / View query / Copy link). The same object that draws
// the chart is the object the explorer re-opens (see lib/metricQuery.ts).
// Grafana-grade unobtrusive: a hover cursor + the ⋯ overflow button only
// appearing on hover; the chart itself is NOT restyled — children render
// verbatim.
//
// Interactions:
//   • header title click / body click  → Explore (metricExploreHref)
//   • ⋯ menu                            → the four actions, all driven by `mq`
//   • keyboard `e` while hovered/focused → Explore
//
// v0.9.1163 — sarmalanan içerik KENDİ ⋯'ünü taşıyorsa (CorePanel) bu
// sarmalayıcı kebabını bastırıp eylemlerini ona DEVREDER: panel başına tek
// tetik, tek dropdown. Bkz. `suppressMenu` + lib/chart/panelMenu.ts.

export interface MetricPanelBaseProps {
  title: string;
  metricQuery: MetricQuery;
  // className/style pass through to the outer wrapper so a caller can size it
  // inside a grid without MetricPanel imposing layout.
  className?: string;
  style?: React.CSSProperties;
  // compact (v0.8.19, Phase C) — the affordance for panels that ALREADY own a
  // title (a KPI tile's .ov-lab, a RED chart's .ov-card-h). Instead of the
  // stacked title-header row, the ⋮ floats top-right over the panel
  // (hover-revealed) and the body stays byte-identical — no extra label, no
  // pushed-down layout. Body-click / `e` / the ⋮ menu still open Explore from
  // the same descriptor. Default (false) keeps the full header used by
  // Metrics.tsx.
  compact?: boolean;
  // menuOnly (v0.8.69, D5) — for INTERACTIVE charts (drag-zoom, bucket-click)
  // where a whole-body click would collide with the chart's own click handler.
  // Suppresses body-click → Explore; the doorway is the hover ⋮ menu + the `e`
  // shortcut only. Non-interactive panels (KPI tiles, previews) keep body-click.
  menuOnly?: boolean;
}

// v0.9.1163 (operatör-raporlu: "servis Overview panellerinde çift ⋯") —
// MENÜYÜ DEVRETME. Sarmalanan içerik KENDİ ⋯'ünü taşıyan bir panelse
// (CorePanel: tam ekran / CSV / sorguyu göster / log) iki tetik aynı köşede
// üst üste geliyordu. `suppressMenu` bu sarmalayıcının kebabını kaldırır ve
// dört kapı eylemini içteki panele devreder.
//
// API GEREKÇESİ — neden çıplak bir boolean DEĞİL: bastırmak, eylemleri
// başka bir yere BAĞLAMADAN yapılabilseydi affordance sessizce ÖLÜRDÜ
// (bu depoda tekrar eden sınıf: "ölü affordance", "yarım kablo
// bırakmıyoruz"). Bu yüzden tip ayrık birleşim: `suppressMenu` FONKSİYON
// çocuk zorunlu kılıyor ve fonksiyon eylemleri ELİNE veriyor —
// bastırıp devretmeyi unutan bir çağrı derlenmez. Kapı tsc'nin kendisi,
// kırılgan bir kaynak taraması değil.
//
// Bastırılmayan çağrılar (KPI karoları, ServiceCharts kartları) bayt bayt
// aynı kalır: kendi hover ⋯'lerini çizmeye devam ederler.
export type MetricPanelProps = MetricPanelBaseProps & (
  | { suppressMenu?: false; children: ReactNode }
  | { suppressMenu: true; children: (actions: PanelMenuAction[]) => ReactNode }
);

type MenuAction = 'explore' | 'edit' | 'view' | 'copy';

export function MetricPanel(props: MetricPanelProps) {
  const { title, metricQuery: mq, className, style, compact = false, menuOnly = false } = props;
  const suppressMenu = props.suppressMenu === true;
  const navigate = useNavigate();
  const [menuOpen, setMenuOpen] = useState(false);
  const [viewOpen, setViewOpen] = useState(false);
  const [linkCopied, setLinkCopied] = useState(false);
  const [hovered, setHovered] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  const rootRef = useRef<HTMLDivElement>(null);

  const href = metricExploreHref(mq);

  // Close the overflow menu on outside click — mirrors the Sidebar user-menu.
  useEffect(() => {
    if (!menuOpen) return;
    const onDoc = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setMenuOpen(false);
      }
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [menuOpen]);

  // Esc closes the "View query" popover.
  // v0.9.950 (E2/Ö28) — KATMAN: panelin altındaki sayfa/çekmece aynı
  // Esc'le kapanmasın.
  useEscLayer(viewOpen, () => setViewOpen(false));

  const explore = () => navigate(href);

  // Body click → Explore, BUT never hijack a real interactive child (a panel
  // toolbar may carry selects / buttons / inputs). If the click landed on or
  // inside an interactive element, let it do its own thing. This keeps the
  // affordance reusable on ANY panel without the panel having to stopPropagation.
  const onBodyClick = (e: React.MouseEvent<HTMLDivElement>) => {
    const interactive = (e.target as HTMLElement).closest(
      'button, a, input, select, textarea, label, [role="button"], [contenteditable="true"]',
    );
    if (interactive) return;
    explore();
  };

  // v0.9.1163 — menüyü artık runAction KAPATMIYOR: kapanış kararı satır
  // sözleşmesine (PanelMenuAction.keepOpen) taşındı, çünkü aynı dört eylem
  // İKİ dropdown'da basılabiliyor (buradaki kendi menümüz + devredildiğinde
  // panelin menüsü) ve iki yerde iki farklı kapanış kuralı bir gün ayrışırdı.
  const runAction = (a: MenuAction) => {
    switch (a) {
      case 'explore':
        navigate(href);
        break;
      case 'edit':
        // Explore IS the builder; &edit=1 is a hint the operator landed here to
        // tweak the query (Explore decodes ?m= into its existing builder state).
        navigate(href + '&edit=1');
        break;
      case 'view':
        setViewOpen(true);
        break;
      case 'copy':
        // Absolute link so it survives a paste into Slack / a runbook.
        // v0.8.550 — the flash used to fire unconditionally next to a
        // `void navigator.clipboard?.writeText(…)`: on a plain-HTTP install
        // the optional chain no-op'd and the menu still said "Copied", so
        // the operator pasted nothing into their runbook. Now it flashes
        // only on a real copy, and the shared helper adds the textarea
        // fallback this surface never had.
        // v0.9.1163 — "flash" artık ayrı bir çip değil, SATIRIN KENDİ
        // etiketi ('⧉ Copied') ve o satır menüyü açık bırakıyor. Eski çip
        // mutlak konumluydu (top:6/right:6) ve devredilmiş modda panelin
        // başlık satırındaki ⋯'ün TAM ÜSTÜNE düşüyordu. Tek kanal, iki
        // modda da görünür; v0.8.550'nin kuralı aynen: yalnız gerçek kopya.
        void copyToClipboard(window.location.origin + href).then(ok => {
          if (!ok) return;
          setLinkCopied(true);
          setTimeout(() => setLinkCopied(false), 1500);
        });
        break;
      // v0.9.1049 (Faz 0.9) — 'dashboard' ve 'alert' kalemleri SÖKÜLDÜ.
      // İkisi de "best-effort, full consume is later" notuyla /dashboards?m=
      // ve /alerts?m='e yönlendiriyordu; iki hedef sayfa da ?m='i HİÇ
      // okumadı — menü vaat edip düşürüyordu (ölü affordance). Gerçek
      // kablolama (MetricQuery→Panel çevirmeni + Alerts ön-dolum tasarımı)
      // ayrı bir dilim; o gelmeden menüde yer almazlar.
    }
  };

  // `e` → Explore when the panel is hovered or focus is inside it. Ignore when
  // typing into a field that happens to be inside the body, and when a modifier
  // is held (so it doesn't steal Cmd/Ctrl shortcuts).
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key !== 'e' || e.metaKey || e.ctrlKey || e.altKey) return;
    const t = e.target as HTMLElement;
    const tag = t.tagName;
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || t.isContentEditable) return;
    e.preventDefault();
    explore();
  };

  // v0.9.1163 — DÖRT EYLEM TEK LİSTEDE tanımlı. Öncesinde satırlar JSX'te
  // elle yazılıydı; devretme gelince aynı dördü ikinci kez (şekil olarak)
  // yazmak gerekirdi ve iki liste bir gün ayrışırdı. Artık tek kaynak: hem
  // bizim dropdown'ımız hem devredilen panel BU diziyi basıyor.
  //
  // Explore adresi `href` = metricExploreHref(mq) — yani v0.9.1161'in
  // withMetricSource sarması (?metricsrc=vm deneme modunun derin linkte
  // taşınması) devredilen satıra da BEDAVA geliyor; ikinci bir adres kurma
  // yolu doğmadığı için bozulması da imkânsız.
  const actions: PanelMenuAction[] = [
    { key: 'explore', label: '⤢ Explore', onClick: () => runAction('explore') },
    { key: 'edit', label: '✎ Edit', onClick: () => runAction('edit') },
    { key: 'view', label: '⟨⟩ View query', onClick: () => runAction('view') },
    {
      key: 'copy',
      label: linkCopied ? '⧉ Copied' : '⧉ Copy link',
      onClick: () => runAction('copy'),
      // Geri bildirim satırın kendisinde okunuyor → menü açık kalmalı.
      keepOpen: true,
    },
  ];

  // The ⋮ overflow button + its dropdown. Shared by both layouts (header vs
  // compact overlay) so the menu reads identically; only the positioning of the
  // enclosing wrapper differs.
  const overflow = (
    <div ref={menuRef} style={{ position: 'relative' }}>
      <IconButton
        aria-label="Panel menu"
        aria-haspopup="menu"
        aria-expanded={menuOpen}
        onClick={() => setMenuOpen(o => !o)}
        variant="secondary" size="sm"
        style={{
          // Reveal on hover / focus-within / when open — Grafana-style.
          opacity: hovered || menuOpen ? 1 : 0,
          transition: 'opacity .12s ease',
        }}
        title="Panel actions"
        // v0.9.890 — glif ⋮ değil ⋯. Bu tetik ile Dashboard'ınki AYNI
        // işti, aynı 26×24 kutuydu ve GLİFLERİ farklıydı; denetim bunu
        // ayrıca kusur olarak işaretledi. İkisi de yatay üç nokta.
        icon="⋯"
      />

      {menuOpen && (
        <div
          role="menu"
          onClick={e => e.stopPropagation()}
          style={{
            position: 'absolute', top: '100%', right: 0, marginTop: 4,
            minWidth: 188, background: 'var(--bg2)',
            border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)',
            boxShadow: 'var(--shadow-pop)', padding: 4, zIndex: 'var(--z-dropdown)',
          }}
        >
          {actions.map(a => (
            <PanelMenuItem key={a.key}
              onClick={() => { if (!a.keepOpen) setMenuOpen(false); a.onClick(); }}>
              {a.label}
            </PanelMenuItem>
          ))}
        </div>
      )}
    </div>
  );

  return (
    <div
      ref={rootRef}
      className={className}
      style={{ position: 'relative', ...style }}
      tabIndex={0}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      onFocus={() => setHovered(true)}
      onBlur={(e) => {
        // Only un-hover when focus actually left the panel subtree.
        if (!rootRef.current?.contains(e.relatedTarget as Node)) setHovered(false);
      }}
      onKeyDown={onKeyDown}
    >
      {/* Header row — title (click → Explore) + ⋮ overflow (hover-revealed).
          Suppressed in compact mode: the wrapped panel already owns its title,
          so the doorway is just the floating ⋮ below + the body click. */}
      {!compact && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
          <button
            type="button"
            onClick={explore}
            title={`Explore — ${describeMetricQuery(mq)} (press e)`}
            style={{
              background: 'transparent', border: 'none', padding: 0,
              font: 'inherit', color: 'var(--text)', fontWeight: 600,
              fontSize: 13, cursor: 'pointer', textAlign: 'left',
              display: 'inline-flex', alignItems: 'center', gap: 6,
            }}
            className="metric-panel-title"
          >
            {title}
          </button>
          <span style={{ flex: 1 }} />
          {!suppressMenu && overflow}
        </div>
      )}

      {/* Compact overlay ⋮ — floats top-right OVER the panel so the wrapped
          tile/chart keeps its own layout (no title row pushes it down).
          v0.9.1163 — devredilmiş modda BU KATMAN HİÇ ÇİZİLMEZ: tetik de,
          eski "Copied" çipi de panelin başlık satırındaki ⋯ ile aynı köşeye
          düşüyordu. Boş bir mutlak katman bırakmak da doğru olmazdı —
          görünmez bir kutu, üstüne düştüğü kontrolün tıkını yutabilir. */}
      {compact && !suppressMenu && (
        <div style={{ position: 'absolute', top: 6, right: 6, zIndex: 4, display: 'flex', alignItems: 'center', gap: 6 }}>
          {overflow}
        </div>
      )}

      {/* Body — the chart/stat, rendered verbatim. Clicking it also Explores;
          children stop propagation themselves if they have own click targets.
          v0.9.1163 — devredilmiş modda çocuk bir FONKSİYON: dört kapı eylemi
          eline verilir, o da sardığı panele menuExtra olarak geçirir. Dal
          `props` üzerinden yazılı (yıkımdan sonra değil) ki TS ayrık birleşimi
          daraltsın — `as` ile zorlamaya gerek yok. */}
      <div
        onClick={menuOnly ? undefined : onBodyClick}
        style={{ cursor: menuOnly ? 'default' : 'pointer' }}
        title={menuOnly ? undefined : 'Open in Explore (press e)'}
      >
        {props.suppressMenu ? props.children(actions) : props.children}
      </div>

      {/* View-query popover — read-only PromQL-style projection + Copy. */}
      {viewOpen && (
        <div
          role="dialog"
          aria-label="Metric query"
          onClick={e => e.stopPropagation()}
          style={{
            position: 'absolute', top: 30, right: 0, zIndex: 'var(--z-popover)',
            minWidth: 280, maxWidth: 460,
            background: 'var(--bg2)', border: '1px solid var(--border)',
            borderRadius: 'var(--radius-sm)', boxShadow: 'var(--shadow-pop)',
            padding: 12,
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
            <span style={{
              fontSize: 10.5, fontWeight: 700, letterSpacing: '.5px',
              color: 'var(--text3)', textTransform: 'uppercase',
            }}>
              Query
            </span>
            <span style={{ flex: 1 }} />
            <CopyButton value={describeMetricQuery(mq)} title="Copy query" />
            <Button
              variant="secondary"
              size="sm"
              aria-label="Close"
              onClick={() => setViewOpen(false)}
            >
              ✕
            </Button>
          </div>
          <pre
            className="mono"
            style={{
              margin: 0, whiteSpace: 'pre-wrap', wordBreak: 'break-word',
              fontSize: 12, color: 'var(--text)', background: 'var(--bg1)',
              border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)',
              padding: '8px 10px',
            }}
          >
            {describeMetricQuery(mq)}
          </pre>
        </div>
      )}
    </div>
  );
}

// PanelMenuItem — same shape as the Sidebar user-menu MenuItem (tokens +
// hover background) so the overflow menu reads identically to the rest of the
// app's dropdowns.
function PanelMenuItem({ children, onClick }: { children: ReactNode; onClick: () => void }) {
  return (
    // v0.9.890 — eskiden JS-hover'lıydı (onMouseEnter/onMouseLeave ile
    // satır içi background). Klavye focus'u HİÇ vurgulanmıyordu: menüde
    // ok tuşlarıyla gezen operatör nerede olduğunu göremiyordu.
    <MenuItem onClick={onClick}>{children}</MenuItem>
  );
}
