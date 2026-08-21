import { useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { api } from '@/lib/api';
import { Button } from '@/components/ui/Button';
import { Spinner, Empty } from '@/components/Spinner';
import { RenderedMarkdown } from '@/components/Markdown';
import { AIFeedbackButtons } from '@/components/ai/AIFeedbackButtons';
import { RCAVerdictPanel } from '@/components/RCAVerdictPanel';
import {
  insightHasEvidence, insightHrefInternal, insightQuestion, insightTone,
} from '@/lib/insightCard';
import type { InsightKind, InsightResponse, InsightSignal } from '@/lib/types';

// InsightCard — gömülü bağlamsal açıklama kartı (v0.9.1130, AI Faz 2.2;
// onaylı mockup "InsightCard — mockup", docs/plans/ai-assistant-design-2026-08-16.md §2.3).
//
// Kart, tıklanan satırın ALTINDA açılır (genişleyen-satır ailesi) —
// liste-üstü bir kart feed'i DEĞİL, tablo yoğunluğu aynen kalır. Bu
// bileşen YALNIZ açık hâli çizer: collapsed satırın pasif özeti host
// sayfanın işi (worker yazımı, sıfır fetch, sıfır LLM maliyeti).
//
// ── ÜÇ YOL, BİRİ DİĞERİNİ BEKLEMEZ ──────────────────────────────────
//
// SINYALLER + PİVOTLAR sunucunun İLK SSE çerçevesinde gelir ve AI kapalı
// olsa da TAMDIR; anlatı sonra token token akar ve HİÇ gelmeyebilir.
// Bileşen bu ayrımı state'inde de koruyor: `base` (deterministik yarı)
// ile `prose` ayrı, ve prose'un hatası base'i EKRANDAN SİLMEZ. Emsal
// ServiceChartsExplainBody (v0.9.1033): model saçmalasa bile tablo doğru
// kalır — orada gerekçe, burada tip + state şekli.
//
// ── MALİYET DİSİPLİNİ ───────────────────────────────────────────────
//
// Fetch YALNIZ mount'ta (host bileşeni satır açılınca mount eder), poll
// YOK, pencere odağında yeniden çekme YOK, re-render'da yeniden çekme
// YOK (`ranRef` özne başına bir kez). Tek yeniden-üretim yolu "↺ Yeniden
// üret" ve o da uçuştaki akışı KESER — kesilmezse eski akışın delta'ları
// yeni cevabın üstüne yazar (v0.9.1127'de CopilotExplain'de pinlenen dal).
//
// ── ?ai= KODEĞİ BİLEREK GENİŞLETİLMEDİ ──────────────────────────────
//
// Mockup'ın ⑦ notu kartın adreste yaşamasını istiyor ve bu DOĞRU hedef,
// ama `?ai=` bugün TEK tüketicili: AppShell'de mount edilen AIDrawer o
// parametreyi görüp sağ-kenar çekmecesini açıyor (AIDrawer.tsx:27).
// AI_KINDS'a `insight-*` eklemek iki somut kırılma üretirdi:
//   1. aynı adres HEM satır kartını HEM çekmeceyi açar (iki AI yüzeyi
//      üst üste — v0.9.479'da operatör bunu bildirmişti);
//   2. çekmece gövdesi CopilotExplain'e düşer ve onun kind zinciri
//      bilinmeyen bir kind'ı SESSİZCE son dala yollar
//      (copilotExplainServiceHealth, CopilotExplain.tsx:167) — yani
//      fingerprint'i SERVİS ADI sanan bir LLM çağrısı.
// Kartın paylaşılabilirliği host'un satır kimliğidir (`?exc=` / `?problem=`
// zaten var ve zaten o satırı açıyor); kodek genişlemesi çekmece tarafında
// bir "kart kind'ını atla" kapısı ister, o da 2.3'ün (yuva) işi. Bu yüzden
// bileşen URL-AGNOSTİK: yalnız prop alır, adres yazmaz.
//
// ── CHARTS[] BU DİLİMDE ÇİZİLMİYOR ──────────────────────────────────
//
// Sunucu doğrulanmış ChartSpec üretiyor (charts.go) ama tek çizici
// CosreChart penceresini `now - rangeS` ile KURUYOR (CosreChart.tsx:36).
// Kartın pencereleri ise geçmişte olabilir (çözülmüş problem, bayat
// exception grubu): 35 dakikalık bir spec'i "şimdi"ye çakmak, olayın
// YANINDA alakasız bir pencere çizmek olurdu — v0.9.208 sınıfının
// (pencere düşüren pivot) grafik hâli. CosreChart mutlak pencere alana
// dek spec'ler tipte taşınıyor, çizilmiyor.
export function InsightCard({ kind, id, windowSec, onClose }: {
  kind: InsightKind;
  id: string;
  /**
   * Kanıt penceresi, SANİYE (v0.9.1137, Faz 2.4). Yalnız pencereye bağlı
   * türler geçirir — `slow-query` sayfanın aralığını taşımak ZORUNDA
   * (çağrı sayısı ve p95 pencere-kapsamlı, yani 1sa varsayılanı 6sa'lık
   * bir sayfada satırdakinden BAŞKA sayı gösterirdi). `log-pattern`
   * bilerek geçirmiyor: satırların geldiği uç 5dk varsayılanıyla koşuyor.
   * Sunucu değeri rung'lar/kelepçeler; verilmezse tür varsayılanı geçerli.
   */
  windowSec?: number;
  onClose?: () => void;
}) {
  // Deterministik yarı — `signals` çerçevesi (ya da buffered gövde).
  const [base, setBase] = useState<InsightResponse | null>(null);
  // Anlatı. null = henüz tek token gelmedi ("düşünüyor" hâli).
  const [prose, setProse] = useState<string | null>(null);
  // 👍/👎 kimliği YALNIZ cevap TAMAMLANINCA dolar: akmakta olan bir cevabı
  // oylamak, henüz okunmamış bir metne not vermek olurdu (v0.9.592 rayı).
  const [exchangeId, setExchangeId] = useState<string | undefined>(undefined);
  const [busy, setBusy] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  const run = () => {
    abortRef.current?.abort();
    const ac = new AbortController();
    abortRef.current = ac;
    setBusy(true);
    setErr(null);
    setProse(null);
    setExchangeId(undefined);
    // `base` BİLEREK korunuyor: deterministik yarı yeniden üretimde de
    // aynı veriyi gösterecek, silmek kartı bir kare boyunca çökertir
    // (satır yükseklik zıplaması) ve operatöre "kanıt kayboldu" der.
    // Yeni signals çerçevesi geldiğinde üstüne yazılır.
    return (async () => {
      try {
        const r = await api.insight(kind, id, {
          signal: ac.signal,
          windowSec,
          onSignals: setBase,
          onDelta: d => setProse(p => (p ?? '') + d),
        });
        // Nihai metin `answer` çerçevesinden gelir ve biriken delta'ları
        // EZER: model reasoning üretip cevabı sonda toparlarsa delta
        // toplamı eksik kalır (explainStream'in aynı sözleşmesi).
        setBase(r);
        setProse(r.prose);
        setExchangeId(r.exchangeId);
      } catch (e: unknown) {
        // İptal HATA DEĞİL: "Yeniden üret" kendi öncülünü keser, unmount
        // da akışı keser. Kullanıcının kendi eylemini kırmızı kutu olarak
        // göstermek yanlış (api.ts CanceledError disiplini, v0.9.603).
        if (ac.signal.aborted) return;
        setErr(e instanceof Error ? e.message : 'Insight üretilemedi');
        // Biriken YARIM anlatı silinir. Yarım bir kök-neden cümlesini tam
        // sanan operatör yanlış yere koşar; sinyaller zaten yerinde.
        setProse(null);
      } finally {
        if (abortRef.current === ac) setBusy(false);
      }
    })();
  };

  // Özne başına TEK üretim. Ref, re-render'da (ve StrictMode'un çift
  // efekt çağrısında) ikinci bir LLM çağrısını engeller; kind/id gerçekten
  // değişirse yeni özne için bir kez daha çalışır.
  //
  // v0.9.1137 — `windowSec` de ÖZNENİN parçası: farklı bir pencere farklı
  // bir sorudur. Operatör kart açıkken aralığı değiştirirse satırlar
  // yeniden çekiliyor, kart da yeniden üretiliyor — yoksa kart, artık
  // ekranda olmayan bir pencerenin sayılarını anlatmaya devam ederdi
  // (satırda 12.345, kartta 4.100 → hangisi doğru?). Bedeli bilinçli:
  // aralık değişimi nadirdir ve kartı YANLIŞ tutmak daha pahalı.
  const ranRef = useRef('');
  useEffect(() => {
    const key = `${kind}:${id}:${windowSec ?? ''}`;
    if (ranRef.current === key) return;
    ranRef.current = key;
    void run();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind, id, windowSec]);

  // Unmount: uçuştaki akışı kes. Satır kapandıktan sonra açık kalan bir
  // SSE bağlantısı, kimsenin okumadığı token'ları akıtmaya devam eder.
  useEffect(() => () => abortRef.current?.abort(), []);

  // Sohbet köprüsü — mevcut `coremetry:ai-ask` olayı (v0.9.165 emsali):
  // kart CopilotChat'i import etmez, chat de kartı bilmez.
  //
  // v0.9.1137 (Faz 2.4) — soru üreteci `insightQuestion`a taşındı. 2.2/2.3'te
  // kart kind'ları ('exception' | 'problem') AIKind'ın ALT KÜMESİYDİ ve
  // aiSubjectQuestion doğrudan çağrılabiliyordu; iki yeni yuva o ilişkiyi
  // kırdı ve aiSubjectQuestion'ın `default` dalı bilinmeyen türü "Bu
  // trace'i açıkla" yapıyor. Tüketici switch gerekçesi lib/insightCard.ts'te.
  const askInChat = () =>
    window.dispatchEvent(new CustomEvent('coremetry:ai-ask', {
      detail: { question: insightQuestion(kind, id) },
    }));

  // İlk çerçeveden ÖNCE patlayan istek (404, 503, ağ): çizecek kanıt yok.
  if (err !== null && base === null) {
    return (
      <div style={cardStyle}>
        <Empty icon="✨" title="Insight üretilemedi" compact
          action={<Button variant="secondary" size="sm" onClick={run}>↺ Yeniden dene</Button>}>
          {err}
        </Empty>
      </div>
    );
  }
  if (base === null) {
    return (
      <div style={cardStyle}>
        <Spinner label="Sinyaller toplanıyor…" hint="anlatı hemen ardından akar" />
      </div>
    );
  }
  if (!insightHasEvidence(base)) {
    return (
      <div style={cardStyle}>
        <Empty icon="✨" title="Bu satır için kanıt bulunamadı" compact
          action={<Button variant="secondary" size="sm" onClick={run}>↺ Yeniden dene</Button>}>
          Sunucu projeksiyonu boş döndü — olay penceresi kayıtların dışında olabilir.
        </Empty>
      </div>
    );
  }

  const waiting = busy && prose === null;
  const streaming = busy && prose !== null;

  return (
    <div style={cardStyle}>
      <section>
        <div style={headStyle}>
          <span>İlişkili sinyaller — deterministik, anında</span>
          {/* Dürüstlük zarfının kart-içi hâli: "hepsi bu mu" sorusu
              sorulmadan cevaplanır (Response.Truncated). */}
          {base.truncated && (
            <span style={{ color: 'var(--warn)' }}
              title="Liste-şekilli sinyaller tavana takıldı; tam liste hedef sayfada">
              · liste kırpıldı
            </span>
          )}
        </div>
        <div style={{ display: 'grid', gap: 5, fontSize: 12 }}>
          {base.signals.map((s, i) => (
            <SignalRow key={`${i}-${s.kind}-${s.label}`} s={s} />
          ))}
        </div>
      </section>

      {/* v0.9.1207 (Faz 6.3) — hazır verdict varsa kanıt-ID'li anlatı
          kartta. exchangeId BİLEREK verilmiyor: oy affordance'ı kartın
          footer'ında zaten var, panelin kendi oyu ikinci ray olurdu;
          verdict'in kendi oyu ✨ Explain yolunda (RootCauseRibbon). */}
      {base.verdict && (
        <section>
          <div style={headStyle}>
            <span>Kök neden hakemi — önceden üretilmiş, kalkanlı</span>
          </div>
          <RCAVerdictPanel v={base.verdict} />
        </section>
      )}

      <section>
        <div style={headStyle}>
          <span>{base.aiOff ? 'Ne oldu' : 'Ne oldu — CoSRE'}</span>
          {/* Operatörün ilk sorusu "bunu hangi model yazdı" (v0.9.1037).
              YALNIZ model adı: "yerel" gibi bir konum iddiası prod'da
              yanlış olur. Boşsa çip HİÇ çizilmez. */}
          {base.model && (
            <span className="chip" style={{ fontSize: 10.5, flexShrink: 0 }}
              title="Bu anlatıyı üreten model">
              <span className="k">model</span>
              <b className="mono">{base.model}</b>
            </span>
          )}
        </div>
        {base.aiOff ? (
          <div style={offNoteStyle}>
            AI yapılandırılmamış — üstteki sinyaller ve pivotlar yine de tam;
            anlatı için Settings → AI'da bir sağlayıcı tanımla.
          </div>
        ) : err !== null ? (
          <div style={errStyle}>
            {err} Yukarıdaki sinyaller yine de ölçülmüş veridir.
          </div>
        ) : waiting ? (
          <Spinner label="CoSRE düşünüyor…" />
        ) : prose !== null && prose.trim() !== '' ? (
          // RenderedMarkdown ŞART: model **kalın** üretiyor ve ham basım
          // yıldızları ekrana döker (v0.9.641 → v0.9.696 → Faz 0.5'te üç
          // kez aynı kusur). pre-wrap YOK: Markdown zaten blok üretiyor,
          // ikisi birlikte satır aralarını ikiye katlar.
          <div style={proseStyle}>
            <RenderedMarkdown text={prose} />
            {streaming && <span className="cm-ai-cursor" />}
          </div>
        ) : (
          <div style={mutedStyle}>
            Model boş yanıt döndürdü. Yukarıdaki sinyaller yine de ölçülmüş veridir.
          </div>
        )}
      </section>

      {base.links.length > 0 && (
        <section>
          <div style={headStyle}>
            <span>Sonraki adım — sunucu-üretimi pivotlar, pencere taşır</span>
          </div>
          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
            {base.links.map(l => <PivotLink key={l.href} label={l.label} href={l.href} />)}
          </div>
        </section>
      )}

      <div style={footStyle}>
        {/* KAPI KİMLİKTİR, ikinci bir aiOff dalı DEĞİL: exchangeId yoksa
            atom kendini çizmez (AIFeedbackButtons sözleşmesi). AI kapalı
            cevap kimlik taşımaz — yani "faydalı mıydı?" sorusu hiç
            sorulmaz, çünkü kaydedilecek bir model cevabı yok. */}
        <AIFeedbackButtons exchangeId={exchangeId} />
        <span>AI çıkarımı — veriyle doğrula</span>
        <Link to="/ai" style={linkStyle}>/ai kaydı</Link>
        <span style={{ flex: 1 }} />
        <Button variant="secondary" size="xs" onClick={run} loading={busy}
          title="Yeni bir üretim çalıştır (yeni ai_calls kaydı)">
          ↺ Yeniden üret
        </Button>
        {!base.aiOff && (
          <Button variant="accent" size="xs" onClick={askInChat}
            title="Bu kart üzerinden CoSRE ile devam et">
            💬 Chat'te devam et →
          </Button>
        )}
        {onClose && (
          <Button variant="ghost" size="xs" onClick={onClose} title="Kartı kapat">
            ▾ kapat
          </Button>
        )}
      </div>
    </div>
  );
}

// SignalRow — etiket + değer. Değer sunucudan HAZIR biçimlenmiş bir dize
// (birim/format kararı orada); kart onu yeniden yorumlamaz.
//
// Signal.Kind BİLEREK BOYANMIYOR: kapalı küme gelecekteki bir ikon dili
// için var, bugün altı ayrı renk basmak şiddet renkleriyle YARIŞIRDI —
// operatör hangi rengin "kötü" demek olduğunu tahmin etmek zorunda kalır.
function SignalRow({ s }: { s: InsightSignal }) {
  const tone = insightTone(s.severity);
  return (
    <div style={{ display: 'flex', gap: 10, alignItems: 'baseline' }}>
      <span style={{
        color: 'var(--text3)', width: 120, flex: 'none', fontSize: 11,
        overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
      }} title={s.label}>{s.label}</span>
      {/* minWidth:0 + overflowWrap: değer KANITIN kendisi, kırpılamaz —
          taşarsa satır kırılır. table-layout:fixed + nowrap sınıfının
          (sessiz kırpma) tersi karar. */}
      <span style={{ minWidth: 0, overflowWrap: 'anywhere' }}>
        {tone ? <span className={`badge ${tone}`}>{s.value}</span> : s.value}
      </span>
    </div>
  );
}

// PivotLink — sunucu-üretimi derin link. Href AYNEN çizilir; FE ne kurar
// ne düzeltir (pencere taşıma disiplini sunucuda, links.go).
function PivotLink({ label, href }: { label: string; href: string }) {
  if (insightHrefInternal(href)) {
    return <Link to={href} style={pivotStyle}>{label}</Link>;
  }
  // Sunucu bugün dış link ÜRETMİYOR; yine de router'a mutlak bir URL
  // vermemek için ayrı dal (insightHrefInternal'ın `//` kapısı).
  return <a href={href} style={pivotStyle} target="_blank" rel="noopener noreferrer">{label}</a>;
}

const cardStyle: React.CSSProperties = {
  borderTop: '1px dashed var(--border)',
  padding: '12px 14px',
  display: 'grid',
  gap: 12,
  background: 'var(--bg2)',
  minWidth: 0,
};

const headStyle: React.CSSProperties = {
  fontSize: 10.5, letterSpacing: '.7px', textTransform: 'uppercase',
  color: 'var(--text3)', display: 'flex', alignItems: 'center', gap: 6,
  marginBottom: 6, flexWrap: 'wrap',
};

const proseStyle: React.CSSProperties = {
  fontSize: 12.5, maxWidth: '78ch', color: 'var(--text)', minWidth: 0,
};

const offNoteStyle: React.CSSProperties = {
  fontSize: 12, color: 'var(--text3)', fontStyle: 'italic',
  border: '1px dashed var(--border)', borderRadius: 'var(--radius-sm)',
  padding: '6px 10px', maxWidth: '60ch',
};

const errStyle: React.CSSProperties = {
  fontSize: 12, color: 'var(--err)', maxWidth: '60ch',
};

const mutedStyle: React.CSSProperties = {
  fontSize: 12, color: 'var(--text3)', maxWidth: '60ch',
};

const footStyle: React.CSSProperties = {
  display: 'flex', gap: 12, alignItems: 'center', flexWrap: 'wrap',
  fontSize: 11, color: 'var(--text3)',
  borderTop: '1px solid var(--border)', paddingTop: 8,
};

// Link görsel dili: ev konvansiyonu satır-içi accent rengi
// (ServiceChartsExplainBody / DBQueriesPanel emsali) — uydurma bir sınıf
// adı testte yakalanmasa SESSİZCE stilsiz link bırakırdı.
const linkStyle: React.CSSProperties = {
  color: 'var(--accent2)', textDecoration: 'none',
};

const pivotStyle: React.CSSProperties = {
  border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)',
  padding: '3px 10px', fontSize: 11.5, color: 'var(--text)',
  background: 'var(--bg1)', textDecoration: 'none',
};
