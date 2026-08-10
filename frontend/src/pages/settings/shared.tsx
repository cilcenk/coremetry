// Shared primitives for the Settings area (extracted verbatim from the
// former monolithic Settings.tsx during the v0.8.x tabbed-area split).
//
// These helpers were defined once in Settings.tsx and consumed by several
// tabs. Pulling them here keeps the per-section files importing a single
// stable module instead of re-declaring the same markup. Behaviour is
// unchanged — these are the exact functions the tabs used before.
import {
  cloneElement, isValidElement, useCallback, useEffect, useId, useRef, useState,
  type ReactElement, type ReactNode,
} from 'react';
import { QueryError } from '@/components/QueryError';

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

// ── Ayar okuma kapısı (v0.9.938, UX denetimi C4) ────────────────────────────
//
// SEKİZ Settings sekmesi ayar okumasını `.catch(() => setLoaded(true))` ile
// yutuyordu: GET başarısız olunca form YÜKLENDİ sayılıp BOŞ çiziliyordu ve
// Kaydet aktifti. Operatörün tek tıkla mevcut ayarın üstüne boş değerler
// yazması mümkündü — denetimdeki tek VERİ KAYBETTİREN bulgu. Sessiz olması
// daha da kötüsüydü: hiçbir yerde "okuyamadım" yazmıyordu, form gerçekten
// boş sanılıyordu.
//
// Kapının üç garantisi:
//   1. Hata AYRI bir hâl — "boş form" ile "okunamadı" bir daha karışamaz.
//   2. Kaydet ERİŞİLEMEZ — hata dalında form hiç çizilmez, yani "kaydete
//      basma" disiplinine değil, render ağacına bağlı.
//   3. Tekrar dene — ileri giden bir yol olmayan hata ekranı yarım çözüm.
//
// Yarış koruması da burada: sekme hızlı değiştirilince geç dönen bir yanıt
// sökülmüş bileşene yazmaya çalışırdı (C3 sınıfı). `alive` bayrağı onu keser.
export function useSettingsLoad<T>(
  load: () => Promise<T>,
  apply: (value: T) => void,
): { loaded: boolean; error: string | null; retry: () => void } {
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  // Ref'ler: çağıranlar bu iki fonksiyonu her render'da yeniden kuruyor
  // (setter fan-out'u closure'da). Dep listesine koysaydık efekt her
  // render'da yeniden koşar, yani sonsuz GET döngüsü olurdu.
  const loadRef = useRef(load); loadRef.current = load;
  const applyRef = useRef(apply); applyRef.current = apply;

  useEffect(() => {
    let alive = true;
    setError(null);
    loadRef.current()
      .then(v => { if (!alive) return; applyRef.current(v); setLoaded(true); })
      // loaded BİLEREK false kalıyor: "okuduk ama boştu" ile "okuyamadık"
      // ayrı hâller ve ikincisinde form çizilmemeli.
      .catch(e => { if (!alive) return; setError(humanize(e)); });
    return () => { alive = false; };
  }, [attempt]);

  const retry = useCallback(() => setAttempt(a => a + 1), []);
  return { loaded, error, retry };
}

// SettingsLoadError — kapının hata ekranı. QueryError'ın (v0.9.858) Settings
// bağlamındaki yüzü: neden tehlikeli olduğunu da söylüyor, çünkü operatör
// "boş görünüyor, kaydedeyim" refleksinden korunmalı.
export function SettingsLoadError({ error, onRetry }: { error: string; onRetry: () => void }) {
  return (
    <QueryError title="Ayarlar okunamadı" message={error} onRetry={onRetry}>
      Bu sekmenin kayıtlı ayarı sunucudan alınamadı. Form <b>bilerek</b>{' '}
      çizilmiyor: boş bir formu kaydetmek mevcut ayarın üstüne boş değerler
      yazardı.
    </QueryError>
  );
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
