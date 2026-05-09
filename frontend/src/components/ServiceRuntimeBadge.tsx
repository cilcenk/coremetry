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
// 8.0.4" / "Node.js 20.10.0" / "Python 3.12". Tries hard to
// surface SOME version even when the SDK didn't fill in the
// canonical field — most OTel SDKs (Java agent, recent .NET)
// stamp process.runtime.version automatically; Go and Python
// require an explicit `WithProcessRuntimeVersion()` setup
// that many services skip, leaving the version inside the
// free-text `process.runtime.description` instead.
//
// Resolution order:
//   1. runtimeVersion — direct, trusted
//   2. runtimeDesc — extract via per-language regex; Go's
//      "go version go1.21.0 darwin/arm64" → "1.21.0"
//   3. service.version (host) → falls through; we don't pick it
//      here because that's the deployment version, not the
//      runtime version.
//   4. Just the language name with no version — better than
//      hiding the badge entirely; the operator at least sees
//      "Go" / "Python" and knows the stack.
function formatRuntime(rt: ServiceRuntime): string {
  const lang = (rt.language || '').toLowerCase();
  const name = friendlyLanguageName(lang) || rt.runtimeName || '';
  let ver = simplifyVersion(rt.runtimeVersion || '');
  if (!ver && rt.runtimeDesc) ver = extractVersionFromDesc(lang, rt.runtimeDesc);
  if (name && ver) return `${name} ${ver}`;
  if (name)        return name;
  if (rt.runtimeName) return rt.runtimeName;
  if (rt.sdkVersion)  return `OTel SDK ${rt.sdkVersion}`;
  return '';
}

// extractVersionFromDesc squeezes a version number out of the
// free-text process.runtime.description. The patterns differ
// per SDK:
//   Go     → "go version go1.21.0 darwin/arm64"
//   Python → "CPython 3.12.1 (main, ...)"
//   Node.js → "v20.10.0" or "Node.js v20.10.0"
//   Ruby   → "ruby 3.2.2 (2023-03-30 ...) ..."
//   Erlang → "Erlang/OTP 26 ... 14.0.4"
//   Java   → typically also has "21.0.1+12-LTS" style — covered
//            by simplifyVersion already, but we re-handle here
//            for completeness.
function extractVersionFromDesc(lang: string, desc: string): string {
  const d = desc.trim();
  if (!d) return '';
  switch (lang) {
    case 'go': {
      // "go version go1.21.0 darwin/arm64" — also handle bare
      // "go1.21.0".
      const m = d.match(/go(\d+\.\d+(?:\.\d+)?)/);
      return m ? m[1] : '';
    }
    case 'python': {
      // "CPython 3.12.1 (main, …)" / "Python 3.11.4"
      const m = d.match(/(?:CPython|Python|PyPy)\s+(\d+\.\d+(?:\.\d+)?)/);
      return m ? m[1] : '';
    }
    case 'nodejs':
    case 'javascript': {
      // "v20.10.0" / "Node.js v20.10.0" / bare "20.10.0"
      const m = d.match(/v?(\d+\.\d+\.\d+)/);
      return m ? m[1] : '';
    }
    case 'ruby': {
      const m = d.match(/ruby\s+(\d+\.\d+\.\d+)/i);
      return m ? m[1] : '';
    }
    case 'java':
    case 'kotlin': {
      // "OpenJDK Runtime Environment Temurin-21.0.1+12 (build 21.0.1+12-LTS)"
      // Pull the first dotted version that doesn't start with 0.
      const m = d.match(/(\d+(?:\.\d+){1,3})/);
      return m ? m[1] : '';
    }
    case 'dotnet':
    case 'csharp': {
      const m = d.match(/(\d+\.\d+\.\d+)/);
      return m ? m[1] : '';
    }
    default: {
      // Generic dotted version anywhere in the string.
      const m = d.match(/(\d+(?:\.\d+){1,3})/);
      return m ? m[1] : '';
    }
  }
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
