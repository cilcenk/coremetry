import type { ServiceRuntime } from '@/lib/types';

// Small "Java 21" / "Go 1.22.5" pill rendered next to a
// service name. Used in two places: the infra panel header on
// /service?name=… (one-off detail) and every row of the
// /services listing (batched fetch). The component is
// intentionally pure-presentation — pass it the runtime
// object, get a span back. The data hook (useServiceRuntime
// for single, useAllServiceRuntimes for batch) lives at the
// call site.
//
// Falls back to nothing visible (returns null) when the SDK
// emitted no usable metadata — avoids "Unknown" placeholder
// noise on services that haven't started exporting OTel
// runtime attributes yet.

export function ServiceRuntimeBadge({
  rt, compact, style,
}: {
  rt: ServiceRuntime | null | undefined;
  // compact = 10px font, no glyph; suitable for dense table
  // rows where the language colour alone is the cue. default
  // = 11px font + glyph (full pill).
  compact?: boolean;
  style?: React.CSSProperties;
}) {
  if (!rt) return null;
  const display = formatRuntime(rt);
  if (!display) return null;
  const titleParts = [
    rt.runtimeDesc,
    rt.host && `host: ${rt.host}`,
    rt.os && `os: ${rt.os}`,
    rt.sdkVersion && `OTel SDK ${rt.sdkVersion}`,
  ].filter(Boolean) as string[];
  return (
    <span title={titleParts.join(' · ')}
      style={{
        display: 'inline-flex', alignItems: 'center', gap: 4,
        fontSize: compact ? 10 : 11,
        padding: compact ? '1px 6px' : '2px 8px',
        background: 'var(--bg3)', border: '1px solid var(--border)',
        borderRadius: 12, color: 'var(--text)',
        fontFamily: 'ui-monospace, monospace',
        whiteSpace: 'nowrap',
        ...style,
      }}>
      {!compact && (
        <span style={{ color: languageColor(rt.language) }}>
          {languageGlyph(rt.language)}
        </span>
      )}
      <span style={{ color: compact ? languageColor(rt.language) : 'inherit' }}>
        {display}
      </span>
    </span>
  );
}

// formatRuntime turns a (language, runtime name, version) trio
// into a one-line label like "Java 21" / "Go 1.22.5" / ".NET
// 8.0.4" / "Node.js 20.10.0" / "Python 3.12". Tries to use the
// most operator-recognisable name for each language rather
// than the SDK-emitted free-text.
function formatRuntime(rt: ServiceRuntime): string {
  const lang = (rt.language || '').toLowerCase();
  const name = friendlyLanguageName(lang) || rt.runtimeName || '';
  const ver = simplifyVersion(rt.runtimeVersion || '');
  if (name && ver) return `${name} ${ver}`;
  if (name)        return name;
  if (rt.runtimeName) return rt.runtimeName;
  if (rt.sdkVersion)  return `OTel ${rt.sdkVersion}`;
  return '';
}

function friendlyLanguageName(lang: string): string {
  switch (lang) {
    case 'go':       return 'Go';
    case 'java':     return 'Java';
    case 'kotlin':   return 'Kotlin';
    case 'dotnet':
    case 'csharp':   return '.NET';
    case 'nodejs':
    case 'javascript':
    case 'webjs':    return 'Node.js';
    case 'python':   return 'Python';
    case 'ruby':     return 'Ruby';
    case 'php':      return 'PHP';
    case 'rust':     return 'Rust';
    case 'erlang':   return 'Erlang';
    case 'swift':    return 'Swift';
    default:         return '';
  }
}

// simplifyVersion strips Java's "+12-LTS" suffix and Go's
// "go" prefix so the badge reads "21" / "1.22.5" rather than
// "21+12-LTS" / "go1.22.5".
function simplifyVersion(v: string): string {
  if (!v) return '';
  v = v.trim();
  if (v.startsWith('go')) return v.slice(2);
  const plusIdx = v.indexOf('+');
  if (plusIdx > 0) v = v.slice(0, plusIdx);
  return v;
}

function languageGlyph(lang?: string): string {
  switch ((lang || '').toLowerCase()) {
    case 'go':         return '◆';
    case 'java':
    case 'kotlin':     return '◢';
    case 'dotnet':
    case 'csharp':     return '⌬';
    case 'nodejs':
    case 'javascript': return '◬';
    case 'python':     return '◇';
    case 'ruby':       return '◈';
    case 'php':        return '◐';
    case 'rust':       return '◉';
    default:           return '·';
  }
}

function languageColor(lang?: string): string {
  switch ((lang || '').toLowerCase()) {
    case 'go':       return '#00ADD8';
    case 'java':
    case 'kotlin':   return '#f89820';
    case 'dotnet':
    case 'csharp':   return '#512BD4';
    case 'nodejs':
    case 'javascript': return '#3C873A';
    case 'python':   return '#3776AB';
    case 'ruby':     return '#CC342D';
    case 'php':      return '#777BB4';
    case 'rust':     return '#CE412B';
    default:         return 'var(--text2)';
  }
}
