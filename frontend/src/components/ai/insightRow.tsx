import { useCallback } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { useEscLayer } from '@/lib/escLayer';
import { InsightCard } from '@/components/ai/InsightCard';
import type { InsightKind } from '@/lib/types';

// insightRow — InsightCard'ın SATIR YUVASI (v0.9.1133, AI Faz 2.3;
// onaylı mockup: satır-altı tıkla-açıl, liste-üstü kart feed'i DEĞİL).
//
// Kart (v0.9.1132) bilerek URL-AGNOSTİK yazıldı: yalnız prop alır. Adres
// disiplini burada, tek yerde — iki host (exception satırı + problem
// satırı) aynı kodeği, aynı Esc katmanını ve aynı "tek kart açık"
// kuralını paylaşıyor. İkinci host kendi yazımını doğurursa (iki ayrı
// param, iki ayrı Esc dinleyicisi) "kapı kapsamı göçte erir" sınıfı
// başlar; o yüzden yuva bir MODÜL, bir kalıp değil.
//
// ── NEDEN `?insight=`, `?ai=` DEĞİL ─────────────────────────────────
//
// `?ai=` TEK tüketicili: AppShell'de mount olan AIDrawer onu görüp sağ
// kenar çekmecesini açıyor (AIDrawer.tsx). Kart kind'larını AI_KINDS'a
// eklemek aynı adreste HEM satır kartını HEM çekmeceyi açardı (v0.9.479'da
// operatörün bildirdiği "iki AI yüzeyi üst üste") — gerekçenin tamamı
// InsightCard.tsx başındaki nottadır.
//
// ── NEDEN HOST'UN MEVCUT SATIR PARAMI DA DEĞİL ──────────────────────
//
// Her iki host'ta da satır kimliği zaten adreste yaşıyor AMA ikisi de
// TAM SAYFA detayı açıyor: `?exc=<fingerprint>` (AnomaliesPage listeyi
// komple bırakıp ProblemDetail'e geçer) ve `?problem=<id>` (Variant-B
// AlertProblemHost). Kartı o paramlara bindirmek, mockup'ın açıkça
// KORUMASINI istediği satır→detay gezinmesini kartla DEĞİŞTİRMEK
// olurdu. `?insight=` üçüncü bir eksen: satır yerinde kalır, kanıt
// altında açılır, ve link paylaşıldığında aynı satır aynı kartla açılır.
//
// ── NEDEN DEĞER `<kind>:<id>` (v0.9.1137, Faz 2.4) ───────────────────
//
// 2.3'te param ÇIPLAK id taşıyordu ve iki host farklı sayfalarda olduğu
// için bu yeterliydi. 2.4 iki yeni yuva ekledi ve tablo değişti:
//
//	/problems  → exception satırı (id = fingerprint)
//	/inbox     → problem satırı  (id = problem id)
//	/anomalies → log deseni      (id = desen ADI)
//	/slow-queries → yavaş sorgu  (id = "<hash>[|<system>]")
//
// Çıplak id ile iki somut kırılma doğuyordu:
//
//  1. KOPYALA-YAPIŞTIR ÇAKIŞMASI. `?insight=p-42` içeren bir adresi
//     /anomalies'e taşımak (ya da yalnız yolu değiştirmek) o sayfada
//     "p-42 adlı desen" kartını açmaya çalışır: sunucu 404 döner ve
//     operatör kırmızı bir kutu görür — yanlış tür, doğru görünen adres.
//  2. AYNI SAYFADA İKİ HOST. Bugün /anomalies yalnız desen taşıyor ama
//     bu sayfa zaten beş akış gösteriyor ve ikinci bir yuva eklendiği
//     gün (trace-op anomalileri en yakın aday) tek param iki host
//     tarafından SAHİPLENİLİRDİ: her ikisi de kendi satır kimliğini
//     arar, biri bulur, diğeri "bilinmeyen id" diye sessizce kapalı
//     kalır — ya da daha kötüsü, id'ler çakışırsa İKİ kart açılır.
//
// Çözüm `?ai=` kodeğinin (aiSubject.ts, v0.9.477) aynısı: değer türü
// TAŞIR ve host YALNIZ kendi türünü açar. Yabancı bir değer kartı
// açmaz — satır sağlam kalır, hiçbir istek gitmez.
//
// Ayraç `:` ve id HAM taşınıyor — kodlama TEK katman, ve o katmanın
// sahibi URLSearchParams. `?ai=` kodeği (aiSubject.ts) kendi içinde bir
// encodeURIComponent daha uyguluyor ama onun GEREKÇESİ var: orada
// ':' ile ayrılmış ÜÇ-BEŞ segment var (span:trace:span,
// charts:svc:from:to:scope), yani id'nin içindeki bir ':' segment
// SAYISINI bozar. Burada segment sayısı SABİT iki ve bölme İLK ':'
// üzerinden — id'nin içinde kaç ':' olursa olsun belirsizlik yok.
//
// İkinci bir katman eklemek ölçüldü ve GÖRÜNÜR ŞEKİLDE kötüydü:
// `encodeURIComponent("Disk full")` → "Disk%20full", sonra
// URLSearchParams '%' karakterini de kodluyor → adres çubuğunda
// `insight=log-pattern%3ADisk%2520full`. İşlevsel olarak çalışıyor
// (okuma iki katmanı da çözüyor) ama bu param KOPYALANIP paylaşılan bir
// adrestir; okunmaz bir değer, operatörün "bu link doğru mu" sorusunu
// cevaplayamaması demek. Tek katmanla: `insight=log-pattern%3ADisk+full`.
export const INSIGHT_PARAM = 'insight';

// KIND_GATE — tür kümesinin TEK yazılışı ve aynı zamanda bir DERLEYİCİ
// KAPISI. `Record<InsightKind, true>` bir tür eklenip burası
// güncellenmezse tsc'yi kırar (eksik anahtar) ve uydurma bir tür de
// giremez (fazla anahtar). Alternatif `as const` dizi + `includes`
// olurdu ama o SESSİZ: birleşime eklenen bir tür diziden düşse
// parseInsightParam onu reddeder ve host'un kartı hiç açılmaz — tip
// sistemi bunu göremez.
//
// Neden types.ts'te değil: o dosya BİLEREK tip-yalnız (sıfır runtime
// export). Kodeği tüketen modül kümenin de sahibi.
const KIND_GATE: Record<InsightKind, true> = {
  exception: true,
  problem: true,
  'log-pattern': true,
  'slow-query': true,
};

export const INSIGHT_KINDS = Object.keys(KIND_GATE) as InsightKind[];
const KIND_SET = new Set<string>(INSIGHT_KINDS);

/** `?insight=` değerinin kanonik hâli: `<kind>:<id>` (id HAM). */
export function formatInsightParam(kind: InsightKind, id: string): string {
  return `${kind}:${id}`;
}

/**
 * parseInsightParam — SAF ayrıştırıcı. Bilinmeyen tür ya da boş id →
 * null (kart açılmaz, istek GİTMEZ).
 *
 * Girdi URLSearchParams'ın ÇÖZDÜĞÜ değer, yani burada ikinci bir
 * yüzde-çözme YOK (gerekçe dosya başında: tek kodlama katmanı).
 *
 * Tür kümesi RUNTIME'da da kontrol ediliyor: elle düzenlenmiş bir
 * adresin `?insight=foo:bar`ı sunucuya HİÇ gitmemeli (route 404
 * verirdi ama istek yine de atılırdı).
 */
export function parseInsightParam(
  raw: string | null | undefined,
): { kind: InsightKind; id: string } | null {
  if (!raw) return null;
  const i = raw.indexOf(':');
  if (i <= 0) return null;
  const kind = raw.slice(0, i);
  // Set üzerinden: `in` operatörü prototip anahtarlarını da geçirir
  // ("toString:x" gibi elle düzenlenmiş bir değer tür sanılırdı).
  if (!KIND_SET.has(kind)) return null;
  // İLK ':'den sonrası TAMAMEN id: problem kimlikleri ':' taşıyabiliyor,
  // naif bir split(':') onları keserdi.
  const id = raw.slice(i + 1);
  if (!id) return null;
  return { kind: kind as InsightKind, id };
}

/**
 * insightParams — SAF kodek: `?insight=` yazar/siler, yabancı her
 * parametreyi korur. `value` KANONİK değer (formatInsightParam çıktısı).
 *
 * `live` (window.location.search) TABAN, `prev` (router konumu) üstüne
 * eklenir: Trace/Dashboard/Traces sayfaları URL'lerini ham
 * `history.replaceState` ile yazdığı için router'ın `prev`i BAYAT bir
 * alt küme olabiliyor ve yalnız prev'i kopyalamak operatörün seçili
 * span'ini adresten silerdi (v0.8.256/265/267'de üç kez gemiye giden
 * yabancı-param kaybı sınıfı). Canlı adres her zaman üst kümedir;
 * router onu geriden takip eder, asla önden gitmez. Emsal:
 * useAiSubject.ts (v0.9.477).
 */
export function insightParams(
  prev: URLSearchParams, live: string, value: string | null,
): URLSearchParams {
  const next = new URLSearchParams(live || prev.toString());
  prev.forEach((v, k) => { if (!next.has(k)) next.append(k, v); });
  if (value) next.set(INSIGHT_PARAM, value);
  else next.delete(INSIGHT_PARAM);
  return next;
}

export interface InsightRowState {
  /**
   * Açık satırın kimliği (fingerprint / problem id / desen adı / stmt
   * kimliği), yoksa null. BAŞKA bir türün değeri adreste duruyorsa da
   * null: o kart bu host'a ait değil.
   */
  openId: string | null;
  /** Aynı satır → kapat, başka satır → oraya taşı (TEK kart açık). */
  toggle: (id: string) => void;
  close: () => void;
}

/**
 * useInsightRow — açık kartın kimliği ADRESTEN okunur/yazılır.
 *
 * Yerel bir `useState` AYNASI BİLEREK YOK: URL tek kaynak. Ayna tutmak,
 * bu deponun üç kez gemiye giden hata sınıfını (URL'den tohumla, geri
 * yazma) davet ederdi — ve sig-guard tartışması da ancak ayna varsa
 * doğar. Ayna olmadığı için "tek kart açık" kuralı da bedava geliyor:
 * bir param tek değer taşır.
 *
 * `kind` (v0.9.1137) host'un TÜRÜ ve iki iş yapıyor: adrese yazılan
 * değere önek olur, ve okurken FİLTRE olur — yabancı türün değeri
 * `openId`i doldurmaz. Böylece dört host tek paramı çakışmadan
 * paylaşıyor (gerekçe dosya başında).
 */
export function useInsightRow(kind: InsightKind): InsightRowState {
  const [searchParams, setSearchParams] = useSearchParams();
  const parsed = parseInsightParam(searchParams.get(INSIGHT_PARAM));
  const openId = parsed && parsed.kind === kind ? parsed.id : null;

  const write = useCallback((id: string | null) => {
    setSearchParams(prev => insightParams(
      prev, typeof window !== 'undefined' ? window.location.search : '',
      id === null ? null : formatInsightParam(kind, id),
    ), { replace: true });
    // replace:true — triyaj sırasında açılıp kapanan kartlar history'ye
    // durak yığmamalı; "geri" operatörü listeye değil bir önceki SAYFAYA
    // götürmeli (ev kuralı, frontend-conventions §4).
  }, [setSearchParams, kind]);

  const close = useCallback(() => write(null), [write]);
  const toggle = useCallback(
    (id: string) => write(openId === id ? null : id), [write, openId]);

  // Esc = kartı kapat. Katman yığını üzerinden (v0.9.950): kendi document
  // dinleyicisini kuran bir yüzey, üstünde açık bir modal/çekmece varken
  // de ateşler ve yanlış katmanı düşürür.
  //
  // Katman YALNIZ bu host'un kartı açıkken kayıtlı: `openId` tür
  // filtresinden geçtiği için, yabancı bir `?insight=` değeri Esc
  // katmanını da kirletmiyor (aksi halde Esc "hiçbir şey yapmıyor" gibi
  // görünürdü — üstteki gerçek katman düşmezdi).
  useEscLayer(openId !== null, close);

  return { openId, toggle, close };
}

/**
 * InsightRowChip — satırdaki "▸ Ne oldu?" affordance'ı.
 *
 * KENDİ TIK HEDEFİ: `stopPropagation` olmadan satırın navigate-on-click'i
 * de ateşlerdi, yani kart açılırken operatör tam sayfa detaya ışınlanırdı.
 * Aynı şey KLAVYEDE de geçerli ve orada daha sinsi — her iki host'un
 * `<tr>`si `role="button"` + `onKeyDown` ile Enter/Space'i satır açma
 * olarak yorumluyor, dolayısıyla çipe odaklanıp Enter'a basmak hem çipi
 * tıklar hem satırı açardı. Bu yüzden keydown da durduruluyor (buton
 * kendi native tıkını yine üretir).
 */
export function InsightRowChip({ open, onToggle, title }: {
  open: boolean;
  onToggle: () => void;
  title?: string;
}) {
  return (
    <Button variant="accent" size="xs"
      aria-expanded={open}
      title={title ?? 'Bu satır için ilişkili sinyalleri topla ve ne olduğunu anlat (AI)'}
      onClick={e => { e.stopPropagation(); onToggle(); }}
      onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') e.stopPropagation(); }}>
      {open ? '▾' : '▸'} Ne oldu?
    </Button>
  );
}

/**
 * InsightRowSlot — kartı tabloda satırın ALTINDA taşıyan `<tr>`.
 *
 * Host bunu KOŞULLU çiziyor (`openId === id && …`), yani kart yalnız
 * açıkken mount olur ve kapanınca unmount edilir. Maliyet disiplini
 * tam burada: mount = tek üretim (ES projeksiyonu + LLM), unmount =
 * uçuştaki akışın iptali. Ne prefetch ne poll — kapalı satır SIFIR
 * istek.
 *
 * `padding: 0` bilinçli: dolgu, kenarlık ve zemin kartın kendi
 * `cardStyle`'ında; td'nin de dolgu eklemesi kartı kabuk içinde ikinci
 * bir çerçeveye sokardı.
 */
export function InsightRowSlot({ kind, id, colSpan, windowSec, onClose }: {
  kind: InsightKind;
  id: string;
  colSpan: number;
  /** Kanıt penceresi (sn) — yalnız pencereye bağlı türler geçirir. */
  windowSec?: number;
  onClose: () => void;
}) {
  return (
    <tr data-insight-row={id}>
      <td colSpan={colSpan} style={{ padding: 0 }}>
        <InsightCard kind={kind} id={id} windowSec={windowSec} onClose={onClose} />
      </td>
    </tr>
  );
}

/**
 * InsightGridSlot — kartın TABLO OLMAYAN yuvası (v0.9.1137, Faz 2.4).
 *
 * Neden ikinci bir yuva: 2.4'ün log deseni host'u bir tablo DEĞİL. /anomalies
 * bu akışları bilerek kart ızgarası olarak çiziyor ("volatile — a row can
 * disappear within a minute", streams.tsx başı), yani orada `<tr>` yok.
 * `<tr>`yi ızgaraya koymak geçersiz HTML: React yalnız ÇALIŞMA ZAMANINDA
 * uyarır, tsc susar ve tarayıcı düğümü satır bağlamı dışına taşır.
 *
 * Neden tek polimorfik yuva (`as` prop'u) DEĞİL: iki DOM sözleşmesi farklı
 * — biri `colSpan` taşıyan bir hücre, diğeri `grid-column` uzatması. Tek
 * bileşende birleştirmek "colSpan'i olan ama td olmayan" gibi anlamsız
 * kombinasyonları tip düzeyinde MÜMKÜN kılardı; iki küçük bileşen ikisini
 * de imkânsız tutuyor.
 *
 * `gridColumn: '1 / -1'` kartı TAM GENİŞLİĞE alıyor ve kart, tıklanan
 * kartın hemen ardındaki ızgara satırına oturuyor — "satırın altında
 * açılır" kuralının ızgara hâli. `minWidth: 0` şart: ızgara çocuğu
 * varsayılan `min-width:auto` ile taşar (uzun ifade/örnek satırı).
 */
export function InsightGridSlot({ kind, id, windowSec, onClose }: {
  kind: InsightKind;
  id: string;
  /** Kanıt penceresi (sn) — yalnız pencereye bağlı türler geçirir. */
  windowSec?: number;
  onClose: () => void;
}) {
  return (
    <div data-insight-row={id} style={{ gridColumn: '1 / -1', minWidth: 0 }}>
      <InsightCard kind={kind} id={id} windowSec={windowSec} onClose={onClose} />
    </div>
  );
}
