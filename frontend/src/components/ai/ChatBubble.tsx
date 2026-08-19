import { useState, type ReactNode } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { api } from '@/lib/api';
import { escapeHTML } from '@/lib/utils';
import { Button } from '@/components/ui/Button';
import type { ChatTurn, ChatStepDetail } from '@/lib/types';
import { CosreChart, type CosreChartSpec } from '@/components/CosreChart';
import { parseChatBlocks, type ChatBlock } from './chatMarkdown';
import { parseStepPreview, fmtPreviewBytes } from './stepPreview';

// ChatBubble — bir sohbet turunun ÇİZİMİ. v0.9.479'da CopilotChat.tsx'ten
// buraya taşındı: AI çekmecesi içindeki sohbet (AIDrawer) aynı balonu
// kullanır — ikinci bir chat implementasyonu YOK. Taşıma sırasında
// davranış değişmedi (mdLite/renderMessage/balon gövdesi birebir);
// yalnız `Turn` tipi lib/types.ts'teki paylaşılan ChatTurn oldu.
//
// Balonun taşıdığı affordance'lar (adım çipleri, RAG kaynakları, derin
// linkler, kopyala, 👍/👎) böylece çekmeceye de bedelsiz gelir.

// mdLite — güvenli hafif markdown: ÖNCE escapeHTML (XSS), sonra 32-hex
// trace id'leri tıklanabilir link (v0.9.419 — href salt hex'ten kurulur,
// injection yüzeyi yok; data-nav ile SPA navigate, sayfa yenilenmez ve
// chat state'i yaşar), sonra `kod` + **kalın**. Satır sonları/madde
// tireleri container'ın white-space:pre-wrap'ıyla korunur.
export function mdLite(raw: string): string {
  return escapeHTML(raw)
    .replace(/\b[0-9a-f]{32}\b/g, '<a href="/trace?id=$&" data-nav="1">$&</a>')
    .replace(/`([^`\n]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*\n]+)\*\*/g, '<b>$1</b>');
}

// MdInline — satır içi işaretlemenin TEK basım noktası (v0.9.1148).
//
// XSS DİSİPLİNİ: dosyada dangerouslySetInnerHTML'in TEK yeri burası ve
// beslediği tek şey mdLite (yani escapeHTML'den GEÇMİŞ) dizesi. Faz 4.2
// öncesi aynı çağrı ÜÇ yerde yazılıydı; tablo hücreleri + başlık +
// liste maddeleri eklenince altı olacaktı. Yeni yüzey açmak yerine
// mevcut disiplin tek bileşende toplandı: bundan sonra "chat HTML'i
// nereden basıyor" sorusunun tek cevabı var, ve kapı testi bunu sayıyor.
function MdInline({ text }: { text: string }) {
  return <span dangerouslySetInnerHTML={{ __html: mdLite(text) }} />;
}

// CodeBlock (v0.9.1148) — ``` fence'inin çizimi. Gövde React ÇOCUĞU
// olarak basılıyor, mdLite'tan GEÇMİYOR: kod literaldir (React kendi
// kaçışını yapar) ve `**` ya da `` ` `` içeren bir SQL parçası
// biçimlenmemeli.
//
// Kopyala butonu yalnız çit KAPANDIYSA görünür: yarım bir bloğu
// kopyalatmak, operatörün sessizce eksik bir komut çalıştırması demek.
// Butonun YOKLUĞU tek başına sessiz bir sinyal olurdu, o yüzden başlık
// şeridi durumu YAZIYOR: akarken "yazılıyor…", akış bitmiş ama çit hiç
// kapanmamışsa "kesildi" (Faz 1.5'in truncation sözlüğü). İkinci hâlde
// "yazılıyor" demek yalan olurdu.
function CodeBlock({ lang, code, open, streaming }: {
  lang: string; code: string; open: boolean; streaming: boolean;
}) {
  const [copied, setCopied] = useState(false);
  const copy = () => {
    navigator.clipboard?.writeText(code).then(() => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1400);
    }).catch(() => {});
  };
  return (
    <div className="cm-md-code">
      <div className="cm-md-code-h">
        <span className="cm-md-code-lang">{lang || 'kod'}</span>
        {open && <span className="cm-md-code-st">{streaming ? 'yazılıyor…' : 'kesildi'}</span>}
        {!open && (
          <Button variant="ghost" size="sm" onClick={copy}
            title="Kodu kopyala" aria-label="Kod bloğunu kopyala"
            className={copied ? 'is-ok' : undefined}
            style={{ padding: '0 6px', fontSize: 12 }}>
            {copied ? '✓' : '⧉'}
          </Button>
        )}
      </div>
      <pre>{code}</pre>
    </div>
  );
}

// MdTable (v0.9.1148) — markdown tablosu GERÇEK tablo.
//
// useDataTable YOK ve bu bilinçli: primitif tipli bir COLS dizisi
// (sortValue/numeric) istiyor, buradaki kolonlar ise modelin o cevapta
// uydurduğu başlıklar — sıralama/yeniden boyutlandırma anlamsız,
// storageKey'i olmayan bir tablo layout'u da kalıcılaştırılamaz. Görsel
// dil yine paylaşılıyor: kaydırma kabı `.table-wrap` (yatay taşma
// TABLONUN kabında kalır, sayfa gövdesi yana kaymaz — v0.9.1078 dersi),
// hizalama `.num` (sağ + tabular-nums), gerisi `.cm-md-table` remap'i.
function MdTable({ block }: { block: Extract<ChatBlock, { kind: 'table' }> }) {
  const cls = (i: number) => (block.align[i] === 'right' ? 'num' : block.align[i] === 'center' ? 'ta-c' : undefined);
  // 100+ satır kuralı (ev kısıtı) BURADA DA geçerli: tool sonucu geniş
  // dönerse model 200 satırlık bir tablo yazabiliyor ve balon o zaman
  // sayfanın en pahalı düğümü olur. Sanallaştırma yanlış araç (satır
  // yüksekliği içerikle değişiyor, kaydırma da sayfada), content-visibility
  // ise bedelsiz: görünmeyen satır layout'a girmez.
  const heavy = block.rows.length > 100;
  const rowStyle = heavy
    ? { contentVisibility: 'auto' as const, containIntrinsicSize: '26px' }
    : undefined;
  return (
    <div className="table-wrap cm-md-tw">
      <table className="cm-md-table">
        <thead>
          <tr>{block.head.map((h, i) => <th key={i} className={cls(i)}><MdInline text={h} /></th>)}</tr>
        </thead>
        <tbody>
          {block.rows.map((r, k) => (
            <tr key={k} style={rowStyle}>
              {r.map((c, i) => <td key={i} className={cls(i)}><MdInline text={c} /></td>)}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

const H_TAG = { 1: 'h4', 2: 'h5', 3: 'h6' } as const;

// renderMessage (v0.9.183, Faz 4.2'de blok çözücüye geçti) — asistan
// metnini çizer: düz metin koşuları mdLite ile (SATIR İÇİ span, balonun
// pre-wrap'ı ve akış imlecinin metne YAPIŞIK durması bu yüzden bozulmaz),
// tablo/fence/başlık/liste blok öğeleriyle, ```chart``` blokları canlı
// <CosreChart> ile.
//
// `streaming` = tur akıyor (turn.pending). Çözücüye geçiyor çünkü yarım
// satır kararı orada (chatMarkdown.ts). Chart bloğunun akış davranışı:
//   akıyor + kapanmamış → "grafik hazırlanıyor" satırı. Eskiden ham JSON
//     düzyazı olarak akıyordu (mdLite yolu) — operatöre gösterilecek bir
//     şey değil.
//   bitti + kapanmamış (yanıt KESİLDİ) → kod bloğu. İçeriği yutmak,
//     kesildiğini göstermekten kötü.
//   kapandı + geçerli spec → grafik. Kapandı + bozuk → atlanır
//     (v0.9.183 kararı, aynen korundu: deterministik üreticinin bozuk
//     çıktısı operatörün sorunu değil).
export function renderMessage(text: string, streaming = false) {
  const blocks = parseChatBlocks(text, streaming);
  const out: ReactNode[] = [];
  blocks.forEach((b, i) => {
    switch (b.kind) {
      case 'text':
        out.push(<MdInline key={i} text={b.text} />);
        break;
      case 'heading': {
        // h1-h3 DEĞİL: balon sayfanın içinde bir kutu, başlık hiyerarşisini
        // ele geçirmemeli (ekran okuyucu için sayfa özeti bozulur).
        const H = H_TAG[b.level];
        out.push(<H key={i} className={`cm-md-h cm-md-h${b.level}`}><MdInline text={b.text} /></H>);
        break;
      }
      case 'list': {
        const L = b.ordered ? 'ol' : 'ul';
        out.push(
          <L key={i} className="cm-md-list">
            {b.items.map((it, k) => <li key={k}><MdInline text={it} /></li>)}
          </L>
        );
        break;
      }
      case 'code': {
        if (b.lang === 'chart') {
          if (b.open) {
            if (streaming) {
              out.push(<span key={i} className="cm-md-wait">grafik hazırlanıyor…</span>);
              break;
            }
          } else {
            try {
              const spec = JSON.parse(b.code.trim()) as CosreChartSpec;
              if (spec && typeof spec.service === 'string' && typeof spec.agg === 'string') {
                out.push(<CosreChart key={i} spec={spec} />);
              }
            } catch { /* bozuk spec — atla (asla crash) */ }
            break;
          }
        }
        out.push(<CodeBlock key={i} lang={b.lang} code={b.code} open={b.open} streaming={streaming} />);
        break;
      }
      case 'table':
        out.push(<MdTable key={i} block={b} />);
        break;
    }
  });
  // Yedek dal İÇERİK YOKLUĞUNA bakıyor, ÇIKTI yokluğuna değil. Fark
  // gerçek: bozuk bir chart bloğu bilinçli olarak ATLANIYOR ve o hâlde
  // `out` boş kalır — `out.length`e bakan bir koşul o an balonun HAM
  // markdown'ını (çitler dahil) ekrana dökerdi. Yedek yalnız "hiç blok
  // çıkmadı" (boş/yalnız-boşluk metin) hâli için var.
  return blocks.length === 0 ? <MdInline text={text} /> : <>{out}</>;
}

// rateTurn — 👍/👎 POST'u. İyimser güncelleme + hata hâlinde geri alma;
// iki yüzey de aynı davranışı paylaşsın diye burada (v0.9.479).
export function rateTurn(
  turns: ChatTurn[], idx: number, verdict: 1 | -1,
  setTurns: (fn: (prev: ChatTurn[]) => ChatTurn[]) => void,
) {
  const turn = turns[idx];
  if (!turn?.exchangeId || turn.verdict === verdict) return;
  const prior = turn.verdict;
  const exchangeId = turn.exchangeId;
  setTurns(prev => prev.map((t, i) => (i === idx ? { ...t, verdict } : t)));
  api.postAIFeedback({ exchangeId, verdict }).catch(() => {
    setTurns(prev => prev.map((t, i) => (i === idx ? { ...t, verdict: prior } : t)));
  });
}

// ToolChips — ⚙ ilerleme çipleri + tıklayınca açılan KANIT bloğu
// (v0.9.1181, AI Faz 4.3).
//
// Neden gerekliydi: çip yalnız tool ADINI gösteriyordu. Model "topolojiye
// baktım, şu servis suçlu" dediğinde operatörün o iddiayı sınayacak hiçbir
// şeyi yoktu — veriyi gördü mü, ne gördü, kırpıldı mı, hepsi görünmezdi.
// Bir APM'de "modele güven" kabul edilebilir bir cevap değil.
//
// Çip yalnız DETAY VARSA düğmeye dönüşür. Arşivden geri yüklenen konuşmalar
// detay taşımaz (chatPersist yalnız {role,text} saklar) ve eski bir sunucuya
// karşı akan FE de taşımaz; ikisinde de çip eskisi gibi düz bir etiket kalır.
// Boş açılan bir "veriyi göster" ölü affordance olurdu (v0.9.592 dersi).
function ToolChips({ steps, details, hasText }: {
  steps: string[];
  details?: ChatStepDetail[];
  hasText: boolean;
}) {
  const [openIdx, setOpenIdx] = useState<number | null>(null);
  // Çip sırası ile detay sırası aynı: ikisi de `step` olayında birlikte
  // büyüyor. Yine de indeksle DEĞİL, dizideki konumla eşliyoruz ve detayın
  // varlığını ayrıca kontrol ediyoruz — kısa detay dizisi (rolling deploy)
  // patlamamalı.
  const detailAt = (i: number) => details?.[i];
  const open = openIdx == null ? undefined : detailAt(openIdx);
  return (
    <div style={{ marginBottom: hasText ? 6 : 0 }}>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
        {steps.map((s, i) => {
          const d = detailAt(i);
          const ready = !!d && d.preview !== undefined;
          const isOpen = openIdx === i;
          const chipStyle: React.CSSProperties = {
            fontSize: 10, fontFamily: 'ui-monospace, monospace',
            padding: '1px 6px', borderRadius: 8,
            background: isOpen ? 'var(--accent-bg)' : 'var(--bg3)',
            color: isOpen ? 'var(--accent2)' : 'var(--text3)',
            border: '1px solid transparent',
          };
          if (!ready) return <span key={i} style={chipStyle}>⚙ {s}</span>;
          return (
            <button key={i} type="button"
              onClick={() => setOpenIdx(isOpen ? null : i)}
              aria-expanded={isOpen}
              title={d?.ok === false ? 'Tool hata döndürdü — veriyi göster' : 'Bu adımın verisini göster'}
              style={{ ...chipStyle, cursor: 'pointer' }}>
              ⚙ {s} {d?.ok === false ? '⚠' : ''}{isOpen ? '▾' : '▸'}
            </button>
          );
        })}
      </div>
      {open && <ToolEvidence d={open} />}
    </div>
  );
}

// ToolEvidence — açılan blok. Modelin GÖRDÜĞÜ metnin kendisi; kırpma
// İLAN EDİLİR (sessiz kırpma, eksik kanıta tam kanıt sanıp bakmak demek).
function ToolEvidence({ d }: { d: ChatStepDetail }) {
  const view = parseStepPreview(d.preview ?? '', d.truncated);
  return (
    <div style={{
      marginTop: 6, padding: '6px 8px', borderRadius: 6,
      background: 'var(--bg1)', border: '1px solid var(--border)',
      fontSize: 11, whiteSpace: 'normal',
    }}>
      {d.args && d.args !== '{}' && (
        <div className="mono" style={{ fontSize: 10, color: 'var(--text3)', marginBottom: 4, wordBreak: 'break-all' }}>
          {d.tool}({d.args})
        </div>
      )}
      {d.truncated && (
        <div className="badge b-warn" style={{ marginBottom: 4 }}>
          kırpıldı · {fmtPreviewBytes(d.bytes ?? 0)} içinden ilk {fmtPreviewBytes(4096)}
        </div>
      )}
      {view.kind === 'table' ? (
        // Kanıt bakışı, veri sayfası DEĞİL — bu yüzden useDataTable yok
        // (sıralama/yeniden boyutlandırma/kalıcı genişlik burada anlamsız).
        // Görünüm dili sohbetin markdown tablosuyla aynı: .cm-md-table.
        <div className="cm-md-tw" style={{ overflowX: 'auto' }}>
          <table className="cm-md-table">
            <thead><tr>{view.cols.map(c => <th key={c}>{c}</th>)}</tr></thead>
            <tbody>
              {view.rows.map((r, i) => (
                <tr key={i}>{r.map((c, j) => <td key={j}>{c}</td>)}</tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <pre className="mono" style={{
          margin: 0, maxHeight: 220, overflow: 'auto',
          whiteSpace: 'pre-wrap', wordBreak: 'break-word',
          fontSize: 10, color: 'var(--text2)',
        }}>{view.text || '(boş)'}</pre>
      )}
    </div>
  );
}

export function ChatBubble({ turn, onRate }: { turn: ChatTurn; onRate?: (v: 1 | -1) => void }) {
  const isUser = turn.role === 'user';
  const navigate = useNavigate();
  const [copied, setCopied] = useState(false);
  // v0.9.419 — mdLite'ın enjekte ettiği data-nav linkleri (trace id'ler)
  // SPA içi gider: tam sayfa yenilenmesi efemer chat'i sıfırlardı.
  const onBodyClick = (e: React.MouseEvent) => {
    const a = (e.target as HTMLElement).closest?.('a[data-nav]');
    if (a) {
      e.preventDefault();
      navigate(a.getAttribute('href') ?? '/');
    }
  };
  const copy = () => {
    navigator.clipboard?.writeText(turn.text ?? '').then(() => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1400);
    }).catch(() => {});
  };
  const done = !isUser && !turn.pending && !turn.error && !!turn.text;
  return (
    <div style={{ alignSelf: isUser ? 'flex-end' : 'flex-start', maxWidth: '85%' }}>
      {/* pre-wrap BURADA KALIYOR ve bu, diğer AI yüzeylerinin tersi
          (orada Markdown blok üretince pre-wrap satır aralığını ikiye
          katlıyordu — v0.9.641/696). Fark şu: balonun düz metin koşuları
          bilinçli olarak SATIR İÇİ span (blok değil), çünkü akış imleci
          metne yapışık durmak zorunda ve kullanıcı balonu da ham metin.
          Satır sonlarını taşıyan tek şey pre-wrap; kaldırılırsa çok
          satırlı cevap tek paragrafa yapışır. Blok öğeler (tablo/kod/
          başlık/liste) kendi margin'lerini `.cm-md-*` üzerinden alıyor. */}
      <div onClick={onBodyClick} style={{
        padding: '8px 11px', borderRadius: 10, fontSize: 13, lineHeight: 1.5,
        whiteSpace: 'pre-wrap', wordBreak: 'break-word',
        background: isUser ? 'var(--accent2)' : 'var(--bg2)',
        color: isUser ? '#fff' : 'var(--text)',
        border: isUser ? 'none' : '1px solid var(--border)',
      }}>
        {/* Tool-call progress chips (assistant only). v0.9.1181 (Faz 4.3):
            veri gelmişse çip TIKLANABİLİR ve altında kanıt bloğu açılır. */}
        {!isUser && turn.steps && turn.steps.length > 0 && (
          <ToolChips steps={turn.steps} details={turn.stepDetails} hasText={!!turn.text} />
        )}
        {turn.error ? (
          <span style={{ color: isUser ? '#fff' : 'var(--err)' }}>⚠ {turn.error}</span>
        ) : isUser ? (
          turn.text
        ) : turn.text ? (
          // Asistan metni: hafif markdown (escape'li) + tablo/fence/başlık/
          // liste blokları (v0.9.1148) + gömülü canlı grafikler (```chart```)
          // + akış sürüyorsa imleç.
          //
          // `turn.pending` çözücüye GEÇİYOR: yarım tablo satırı / kapanmamış
          // çit kararı ona bağlı (chatMarkdown.ts). Bayrağı bağlamayı unutmak
          // testten geçen ama ekranda titreyen bir render verirdi — bu yüzden
          // ChatBubble.render.test.tsx bunu pending=true/false çiftiyle
          // çalışma zamanında ölçüyor ("saf test ≠ BAĞLANMA" dersi).
          <>
            {renderMessage(turn.text, turn.pending)}
            {turn.pending && <span className="cm-ai-cursor" />}
          </>
        ) : turn.pending ? (
          <span style={{ color: 'var(--text3)' }}>yazıyor<span className="cm-ai-cursor" /></span>
        ) : ''}
      </div>

      {/* Kaynak chip'leri (RAG dayanağı). v0.9.515 (operatör): doküman
          ADI çipte GÖSTERİLMİYOR — dosya adı iç artefakt, cevabın parçası
          değil. Çip yine de duruyor ki cevabın bir dokümana dayandığı
          görünsün; ad ipucuna (hover) taşındı, yani denetlenebilirlik
          kaybolmadan gürültü kalktı. */}
      {!isUser && !!turn.sources?.length && !turn.pending && !turn.error && (
        <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap', marginTop: 4 }}>
          {turn.sources.map((src, i) => src.ref ? (
            <a key={i} href={src.ref} target="_blank" rel="noopener"
              className="badge b-info" style={{ textDecoration: 'none', fontSize: 10 }}
              title={`${src.doc} §${src.chunk} · benzerlik ${(src.score * 100).toFixed(0)}%`}>
              📄 Kaynak §{src.chunk}
            </a>
          ) : (
            <span key={i} className="badge b-info" style={{ fontSize: 10 }}
              title={`${src.doc} §${src.chunk} · benzerlik ${(src.score * 100).toFixed(0)}%`}>
              📄 Kaynak §{src.chunk}
            </span>
          ))}
        </div>
      )}

      {/* Derin-link çipleri (v0.9.419) — cevabın konusuna tek tık.
          Sunucu rotadan deterministik üretir; SPA Link, chat yaşar. */}
      {!isUser && !!turn.links?.length && !turn.pending && !turn.error && (
        <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap', marginTop: 4 }}>
          {turn.links.map((l, i) => (
            // v0.9.709 — DIŞ URL çipi (log köprüsü, https://...) SPA
            // <Link>'e verilemez: router onu path sanıp uygulama içinde
            // gezinir ve link kırılır. Dış href <a target=_blank>.
            /^https?:/i.test(l.href) ? (
              // v0.9.1143 — dış çip (Logizleme köprüsü) kanıta atlama
              // yolu; operatör isteğiyle iç çiplerden belirgin büyük.
              <a key={i} href={l.href} target="_blank" rel="noopener noreferrer"
                className="badge b-info"
                style={{ textDecoration: 'none', fontSize: 12, padding: '3px 8px' }}>
                🔗 {l.label}
              </a>
            ) : (
              <Link key={i} to={l.href} className="badge b-info"
                style={{ textDecoration: 'none', fontSize: 10 }}>
                🔗 {l.label}
              </Link>
            )
          ))}
        </div>
      )}

      {/* Aksiyon satırı — copy + thumbs (tamamlanmış asistan cevabı) */}
      {done && (
        <div style={{ display: 'flex', gap: 2, marginTop: 2, alignItems: 'center' }}>
          <Button variant="ghost" size="sm" onClick={copy}
            title="Kopyala" aria-label="Cevabı kopyala"
            className={copied ? 'is-ok' : undefined}
        style={{ padding: '0 6px', fontSize: 12 }}>
            {copied ? '✓' : '⧉'}
          </Button>
          {!!turn.exchangeId && onRate && (
            <>
              <Button variant="ghost" size="sm" onClick={() => onRate(1)}
                title="Faydalı" aria-label="Cevabı faydalı işaretle"
                style={{ padding: '0 6px', fontSize: 12, opacity: turn.verdict === 1 ? 1 : 0.4 }}>👍</Button>
              <Button variant="ghost" size="sm" onClick={() => onRate(-1)}
                title="Faydasız" aria-label="Cevabı faydasız işaretle"
                style={{ padding: '0 6px', fontSize: 12, opacity: turn.verdict === -1 ? 1 : 0.4 }}>👎</Button>
            </>
          )}
        </div>
      )}
    </div>
  );
}
