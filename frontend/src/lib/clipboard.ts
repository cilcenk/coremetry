// clipboard — one copy-to-clipboard path for the whole app (v0.8.547).
//
// Coremetry ships to on-prem/air-gapped installs served over plain HTTP,
// where `navigator.clipboard` is undefined: the Clipboard API needs a
// secure context (HTTPS or localhost). Every surface therefore needs a
// fallback, and before this file five of eight had none — including the
// one-shot API token, where a silent no-op meant the operator lost the
// token for good.
//
// This is Trace.tsx's version, which the audit measured as the strongest of
// the three hand-rolled copies: it falls back when writeText REJECTS, not
// only when it is missing. The other two treated a rejection as a dead end
// (permission denied, document not focused, iOS quirks — all reject rather
// than vanish).
//
// Returns whether the text actually made it, so callers can flash on
// success and stay honest on failure instead of claiming "Copied"
// unconditionally.
export async function copyToClipboard(text: string): Promise<boolean> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch { /* fall through to the legacy path */ }
  }
  return fallbackCopy(text);
}

// Hidden-textarea + execCommand. Deprecated, and the only thing that works
// without a secure context.
function fallbackCopy(text: string): boolean {
  try {
    const ta = document.createElement('textarea');
    ta.value = text;
    // `fixed` + transparent keeps the select() from scrolling the page.
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    ta.remove();
    return ok;
  } catch {
    return false;
  }
}
