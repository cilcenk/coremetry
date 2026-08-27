import { idLinkPattern, type IdLink, type IdLinkPattern } from '@/components/ai/inlineIdLinks';
import { normalizeMathEscapes } from './mathEscapes';
// RenderedMarkdown — a deliberately small markdown renderer extracted
// from the old Notebook page (v0.7.0, when Notebook was replaced by
// Runbooks). Handles the subset operators actually use in incident
// notes / runbook descriptions + step instructions: # / ## / ###
// headings, **bold**, *italic*, `code`, [link](url), - bullets, and
// ``` fenced blocks. Unknown markdown passes through as-is so the
// operator isn't surprised by silently-stripped content.
//
// Kept intentionally dependency-free (no marked / remark) — the input
// is short and we never render untrusted HTML.
//
// ⚠ v0.10.12 — "operator-authored" VARSAYIMI DÜŞTÜ. Bu renderer artık
// LLM çıktısını da basıyor (CopilotExplain'in "Explain root cause"
// paneli) ve model operatör değil: küçük yerel modeller LaTeX kaçışı
// üretiyor. Operatör bildirdi — akış zinciri ekranda
// `svc-a $\rightarrow$ svc-b` diye görünüyordu, yani okunması gereken
// TEK satır (hata hangi servisten hangisine yayıldı) gürültüye
// gömülmüştü. `normalizeMathEscapes` girişte bir kez uygulanıyor;
// gerekçesi ve neden prompt'la çözülmediği o dosyada.
export function RenderedMarkdown({ text, idLinks }: {
  text: string;
  // v0.10.35 — SUNUCUNUN linklenebilir saydığı kimlikler. Verilirse
  // metinde geçtikleri yerde satır içi link olurlar. Hangi kimliğin
  // linklenebilir olduğuna sunucu karar veriyor (anahtar-kelime kapısı,
  // şablon geçerliliği, ortam); burası yalnız DEKORASYON.
  idLinks?: IdLink[];
}) {
  const idPat = idLinkPattern(idLinks);
  const blocks: React.ReactNode[] = [];
  const lines = normalizeMathEscapes(text).split('\n');
  let i = 0;
  let bulletBuf: string[] = [];
  const flushBullets = () => {
    if (bulletBuf.length === 0) return;
    blocks.push(
      <ul key={blocks.length} style={{ paddingLeft: 20, margin: '6px 0' }}>
        {bulletBuf.map((b, k) => <li key={k}>{renderInline(b, idPat)}</li>)}
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
      blocks.push(<p key={blocks.length} style={{ margin: '4px 0' }}>{renderInline(line, idPat)}</p>);
    }
    i++;
  }
  flushBullets();
  // Kök artık fragment DEĞİL: overflow-wrap kalıtsal ve tek kalıtım
  // noktası uzun nitelikli adların balonu yana itmesini bitiriyor
  // (v0.10.79, gerekçe globals.css .cm-md-wrap).
  return <div className="cm-md-wrap">{blocks}</div>;
}

// stripMarkdown — işaretleri ATAR, düz metin döndürür (v0.9.696).
//
// Operatör-bildirimi: "Burada neden açıklama da hata tipi falan bold
// yazmıyor" — exception detayında modelin `* **Hata Tipi:** …` çıktısı
// yıldızlarıyla görünüyordu.
//
// v0.9.641 aynı sınıfı CopilotExplain için düzeltmişti ama AI metni BEŞ
// yerde basılıyor ve o kapı yalnız BİRİNİ kapsıyordu.
//
// NEDEN İKİ ARAÇ GEREKİYOR: yüzeylerin yarısı KIRPILMIŞ tek satırlık
// kart önizlemesi (WebkitLineClamp) ve `title=` ipucu. RenderedMarkdown
// oralarda YANLIŞ: <p>/<ul> blokları üretiyor, satır kırpması bozuluyor;
// `title` ise bir HTML özniteliği, içine React düğümü koyulamıyor —
// oraya markdown'ı ancak DÜZLEŞTİREREK sokabilirsin. Yani "her yerde
// RenderedMarkdown kullan" kuralı kartlarda düzeni bozardı.
//
// Bilinmeyen işaretler OLDUĞU GİBİ kalıyor — RenderedMarkdown'ın
// "sessizce içerik yutma" ilkesiyle aynı.
export function stripMarkdown(s: string): string {
  return s
    // ``` çitleri: yalnız çit satırı gider, kod durur.
    .replace(/^```.*$/gm, '')
    // Başlıklar ve madde imleri satır BAŞINDA.
    .replace(/^\s{0,3}#{1,6}\s+/gm, '')
    .replace(/^\s{0,3}[-*]\s+/gm, '')
    // [etiket](url) → etiket. URL düz metinde gürültü.
    .replace(/\[([^\]]+)\]\(([^)]+)\)/g, '$1')
    // **kalın** / *eğik* / `kod` → içerik. Kapanışsız işaret DURUR:
    // eşleşme çifti şart, yoksa satırın kalanını yutardı.
    .replace(/\*\*([^*]+)\*\*/g, '$1')
    .replace(/\*([^*]+)\*/g, '$1')
    .replace(/`([^`]+)`/g, '$1')
    // Kırpılmış tek satırda satır sonu görsel boşluk demek.
    .replace(/\s*\n+\s*/g, ' ')
    .trim();
}

// Inline markdown — bold, italic, inline code, links. Walks the
// string once, emitting React fragments. The regex is anchored to
// each delimiter so unmatched ones (** without closing **) pass
// through unchanged rather than swallowing the rest of the line.
function renderInline(s: string, idPat?: IdLinkPattern | null): React.ReactNode[] {
  const out: React.ReactNode[] = [];
  let rest = s;
  let key = 0;
  // Order matters: link regex before bold/italic so [**bold**](url)
  // doesn't get consumed by the bold pass first.
  const patterns: { re: RegExp; render: (m: RegExpMatchArray) => React.ReactNode }[] = [
    // v0.10.35 — KİMLİK SATIR İÇİ LİNKİ, markdown desenlerinden ÖNCE:
    // kimlikler nokta/tire içerebiliyor ve italik/bold geçişleri onları
    // ortadan bölerdi. href sunucudan geliyor (aynı köprü, çipin
    // kullandığının aynısı) — burada yeni link mantığı YOK.
    ...(idPat ? [{
      re: idPat.re,
      render: (m: RegExpMatchArray): React.ReactNode => {
        const l = idPat.byId.get(m[1]);
        if (!l) return <span key={key++}>{m[1]}</span>;
        return (
          <a key={key++} href={l.href} target="_blank" rel="noopener noreferrer"
             title={l.label}
             style={{ color: 'var(--accent2)', fontFamily: 'ui-monospace, monospace' }}>
            {m[1]}
          </a>
        );
      },
    }] : []),
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
