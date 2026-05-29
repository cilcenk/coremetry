// RenderedMarkdown — a deliberately small markdown renderer extracted
// from the old Notebook page (v0.7.0, when Notebook was replaced by
// Runbooks). Handles the subset operators actually use in incident
// notes / runbook descriptions + step instructions: # / ## / ###
// headings, **bold**, *italic*, `code`, [link](url), - bullets, and
// ``` fenced blocks. Unknown markdown passes through as-is so the
// operator isn't surprised by silently-stripped content.
//
// Kept intentionally dependency-free (no marked / remark) — the input
// is short, operator-authored, and we never render untrusted HTML.
export function RenderedMarkdown({ text }: { text: string }) {
  const blocks: React.ReactNode[] = [];
  const lines = text.split('\n');
  let i = 0;
  let bulletBuf: string[] = [];
  const flushBullets = () => {
    if (bulletBuf.length === 0) return;
    blocks.push(
      <ul key={blocks.length} style={{ paddingLeft: 20, margin: '6px 0' }}>
        {bulletBuf.map((b, k) => <li key={k}>{renderInline(b)}</li>)}
      </ul>
    );
    bulletBuf = [];
  };
  while (i < lines.length) {
    const line = lines[i];
    if (line.startsWith('```')) {
      // fenced code block
      flushBullets();
      i++;
      const code: string[] = [];
      while (i < lines.length && !lines[i].startsWith('```')) {
        code.push(lines[i]);
        i++;
      }
      blocks.push(
        <pre key={blocks.length} style={{
          padding: 8, background: 'var(--bg)', borderRadius: 4,
          fontSize: 12, overflowX: 'auto',
          fontFamily: 'ui-monospace, SFMono-Regular, monospace',
        }}>{code.join('\n')}</pre>
      );
      i++; continue;
    }
    if (line.startsWith('### ')) {
      flushBullets();
      blocks.push(<h3 key={blocks.length} style={{ margin: '8px 0 4px' }}>{renderInline(line.slice(4))}</h3>);
    } else if (line.startsWith('## ')) {
      flushBullets();
      blocks.push(<h2 key={blocks.length} style={{ margin: '10px 0 4px' }}>{renderInline(line.slice(3))}</h2>);
    } else if (line.startsWith('# ')) {
      flushBullets();
      blocks.push(<h1 key={blocks.length} style={{ margin: '12px 0 6px', fontSize: 18 }}>{renderInline(line.slice(2))}</h1>);
    } else if (line.match(/^[-*] /)) {
      bulletBuf.push(line.slice(2));
    } else if (line.trim() === '') {
      flushBullets();
      blocks.push(<div key={blocks.length} style={{ height: 6 }} />);
    } else {
      flushBullets();
      blocks.push(<p key={blocks.length} style={{ margin: '4px 0' }}>{renderInline(line)}</p>);
    }
    i++;
  }
  flushBullets();
  return <>{blocks}</>;
}

// Inline markdown — bold, italic, inline code, links. Walks the
// string once, emitting React fragments. The regex is anchored to
// each delimiter so unmatched ones (** without closing **) pass
// through unchanged rather than swallowing the rest of the line.
function renderInline(s: string): React.ReactNode[] {
  const out: React.ReactNode[] = [];
  let rest = s;
  let key = 0;
  // Order matters: link regex before bold/italic so [**bold**](url)
  // doesn't get consumed by the bold pass first.
  const patterns: { re: RegExp; render: (m: RegExpMatchArray) => React.ReactNode }[] = [
    { re: /^\[([^\]]+)\]\(([^)]+)\)/,
      render: m => {
        // Scheme allowlist — this markdown is operator-authored and rendered to
        // OTHER users (viewers read editor-authored runbooks), so a
        // `javascript:` href would be a stored-XSS vector. Allow only
        // http(s)/mailto/relative/anchor; otherwise drop to plain text.
        const href = m[2].trim();
        const safe = /^(https?:|mailto:|\/|#)/i.test(href);
        return safe
          ? <a key={key++} href={href} target="_blank" rel="noopener noreferrer"
               style={{ color: 'var(--accent2)' }}>{m[1]}</a>
          : <span key={key++}>{m[1]}</span>;
      } },
    { re: /^\*\*([^*]+)\*\*/,
      render: m => <b key={key++}>{m[1]}</b> },
    { re: /^\*([^*]+)\*/,
      render: m => <i key={key++}>{m[1]}</i> },
    { re: /^`([^`]+)`/,
      render: m => <code key={key++} style={{
        background: 'var(--bg)', padding: '0 4px', borderRadius: 3,
        fontFamily: 'ui-monospace, SFMono-Regular, monospace', fontSize: 12,
      }}>{m[1]}</code> },
  ];
  while (rest.length > 0) {
    let matched = false;
    for (const p of patterns) {
      const m = rest.match(p.re);
      if (m) {
        out.push(p.render(m));
        rest = rest.slice(m[0].length);
        matched = true;
        break;
      }
    }
    if (!matched) {
      out.push(rest[0]);
      rest = rest.slice(1);
    }
  }
  return out;
}
