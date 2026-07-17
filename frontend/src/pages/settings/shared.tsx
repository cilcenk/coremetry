// Shared primitives for the Settings area (extracted verbatim from the
// former monolithic Settings.tsx during the v0.8.x tabbed-area split).
//
// These helpers were defined once in Settings.tsx and consumed by several
// tabs. Pulling them here keeps the per-section files importing a single
// stable module instead of re-declaring the same markup. Behaviour is
// unchanged — these are the exact functions the tabs used before.
import { cloneElement, isValidElement, useId, type ReactElement, type ReactNode } from 'react';

// ── Tiny shared form atoms ──────────────────────────────────────────────────

export function Field({ label, children, flex }: { label: string; children: ReactNode; flex?: number }) {
  return (
    <label style={{ display: 'block', marginBottom: 12, flex }}>
      <div style={{ fontSize: 11, color: 'var(--text2)', marginBottom: 4 }}>{label}</div>
      {children}
    </label>
  );
}

// v0.8.538 — flexWrap so a row that cannot fit drops its overflow onto a
// second line instead of squeezing past the panel edge. Rows that already
// fit are untouched: flex-wrap only engages once the line overflows.
export function Row({ children }: { children: ReactNode }) {
  return (
    <div style={{ display: 'flex', gap: 12, alignItems: 'flex-start', flexWrap: 'wrap' }}>
      {children}
    </div>
  );
}

// v0.8.303 (quality bar U4) — same severity tokens as the toast system
// (color-mix over --ok/--err) so inline settings alerts and toasts read
// as one alert family; the old dark-palette rgba literals never
// re-resolved under the light/redhat themes.
export function FlashBox({ kind, children }: { kind: 'ok' | 'err'; children: ReactNode }) {
  const tok = kind === 'ok' ? 'var(--ok)' : 'var(--err)';
  return (
    <div style={{
      color: tok, fontSize: 12, marginTop: 12,
      padding: '6px 10px',
      background: `color-mix(in srgb, ${tok} 8%, transparent)`,
      border: `1px solid color-mix(in srgb, ${tok} 30%, transparent)`,
      borderRadius: 4,
    }}>{children}</div>
  );
}

// ── shared form atoms (LDAP tab — kept alongside the rest of the
//    Settings shared primitives during the split).
export function SectionTitle({ children }: { children: ReactNode }) {
  return (
    <div style={{
      marginTop: 16, marginBottom: 8,
      fontSize: 12, fontWeight: 600, color: 'var(--text2)',
      textTransform: 'uppercase', letterSpacing: '0.5px',
    }}>{children}</div>
  );
}

// v0.8.538 — minWidth follows `small`. It used to be a flat 200px, which
// sat ABOVE small's own 180px flex-basis: min-width is the floor the flex
// shrink algorithm honours, so a `small` field could never reach its basis
// and the flag did nothing. 140px is the floor a small field actually
// needs — the narrowest content is LdapTab's Team regex, whose monospace
// placeholder "-([^-]+)$" is ~70px of glyphs plus input padding.
// v0.9.15 (a11y) — etiket artık gerçek <label htmlFor>: tek çocuk
// bir element ise useId'li id enjekte edilir (kendi id'si varsa o
// korunur) — etikete tıklamak inputu odaklar, ekran okuyucu
// eşleşmesi kurulur. Field.tsx'in useId sözleşmesinin, çocuğu
// çağıranın sahiplendiği varyantı. Çocuk element değilse (çoklu
// node) label htmlFor'suz kalır — davranış eskisi gibi, semantik
// yine kazanır.
export function Field2({ label, hint, small, children }: {
  label: string; hint?: string; small?: boolean; children: ReactNode;
}) {
  const autoId = useId();
  let control: ReactNode = children;
  let labelFor: string | undefined;
  if (isValidElement<{ id?: string }>(children)) {
    const childId = children.props.id ?? autoId;
    labelFor = childId;
    control = children.props.id
      ? children
      : cloneElement(children as ReactElement<{ id?: string }>, { id: childId });
  }
  return (
    <div style={{ flex: small ? '0 1 180px' : 1, minWidth: small ? 140 : 200 }}>
      <label htmlFor={labelFor}
        style={{ display: 'block', fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>
        {label}
      </label>
      {control}
      {hint && <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4, lineHeight: 1.4 }}>{hint}</div>}
    </div>
  );
}

// humanize — pull a clean message out of a fetch error. Strips the
// "HTTP NNN:" prefix our api client prepends and, if the body is a
// JSON {error} envelope, surfaces just the message.
export function humanize(err: unknown): string {
  const msg = err instanceof Error ? err.message : String(err);
  const body = msg.replace(/^HTTP \d+:\s*/, '');
  try {
    const j = JSON.parse(body);
    if (j && typeof j.error === 'string') return j.error;
  } catch {}
  return body || msg;
}

// ── New reusable section/row wrappers (operator request) ────────────────────
//
// SettingsSection — a titled card wrapper used to group related controls.
// Mirrors the inline `padding:16; borderRadius:8; background:var(--bg2);
// border:1px solid var(--border)` card the tabs hand-rolled, plus an
// optional heading + description so each section reads consistently.
//
// SettingRow — a label + control + optional hint row, the same shape the
// Tempo / Kibana / AI forms used inline (label div over the control over a
// muted hint). Adopt where it's a clean win; the existing inline markup is
// left untouched where a row doesn't fit (grids, checkboxes, tables).
export function SettingsSection({
  title, description, children, maxWidth,
}: {
  title?: ReactNode;
  description?: ReactNode;
  children: ReactNode;
  maxWidth?: number;
}) {
  return (
    <div style={{ maxWidth }}>
      {title && <h2 style={{ fontSize: 14, fontWeight: 600, marginBottom: description ? 6 : 12 }}>{title}</h2>}
      {description && (
        <p style={{ color: 'var(--text2)', fontSize: 13, marginBottom: 16 }}>{description}</p>
      )}
      <div style={{
        padding: 16, borderRadius: 8,
        background: 'var(--bg2)', border: '1px solid var(--border)',
      }}>
        {children}
      </div>
    </div>
  );
}

export function SettingRow({ label, hint, children }: {
  label: ReactNode;
  hint?: ReactNode;
  children: ReactNode;
}) {
  return (
    <label style={{ display: 'block', marginBottom: 12 }}>
      <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 4 }}>{label}</div>
      {children}
      {hint && <div style={{ fontSize: 11, color: 'var(--text3)', marginTop: 4, lineHeight: 1.5 }}>{hint}</div>}
    </label>
  );
}
