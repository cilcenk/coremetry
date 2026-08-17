// chatMarkdown — sohbet balonunun BLOK ÇÖZÜCÜSÜ (v0.9.1148, AI Faz 4.2).
//
// NEDEN VAR: Faz 3 tool'ları geldikten sonra model artık tool
// sonuçlarından TABLO ve KOD üretiyor ("hangi servisler yavaş" → 3
// kolonlu markdown tablosu, "sorguyu göster" → ```sql fence). Balon o
// güne dek mdLite'tı: `kod` + **kalın** + trace linki, gerisi düz metin.
// Sonuç ekranda ham `| a | b |` satırları ve çıplak ``` çitleriydi —
// tam olarak v0.9.641/696'nın "yıldızlar görünüyor" kusurunun tablo
// sürümü.
//
// NEDEN RenderedMarkdown DEĞİL: components/Markdown.tsx zaten var ve
// başlık/liste/fence biliyor, ama chat'e üç şeyi getiremiyor:
//   1. AKIŞ. Balon SSE delta'larıyla harf harf büyüyor. RenderedMarkdown
//      metni "bitmiş" varsayar: yarım bir fence'i kapanmış sayar ve
//      satırın kalanını yutar, yarım bir tablo satırını ham basar.
//      Burada çözücü `streaming` bayrağını alıyor ve BİTMEMİŞ son satırı
//      biliyor (aşağıdaki "kısmi satır" bölümü).
//   2. ```chart fence'i. Balonun kendine ait bir anlamı var (CosreChart,
//      v0.9.183) — Markdown.tsx onu kod bloğu sanar ve operatöre ham
//      JSON döker.
//   3. trace-id linkleri (mdLite, v0.9.419). Markdown.tsx'e eklemek
//      runbook/exception yüzeylerini de değiştirirdi.
// Yani paylaşım DOĞRU katmanda: çözücü chat'e özel, satır-içi kaçış
// disiplini (mdLite → escapeHTML) ORTAK ve değişmedi.
//
// SAFLIK: burada React yok, DOM yok. Çizim ChatBubble'da; bu dosya
// yalnız metin → blok listesi. Kısmi-akış vakalarının hepsi bu yüzden
// saf testle çivilenebiliyor (chatMarkdown.test.ts).

export type ChatBlockAlign = 'left' | 'center' | 'right';

export type ChatBlock =
  // Düz metin koşusu — mdLite ile satır içi işaretlenir, balonun
  // pre-wrap'ı satır sonlarını korur (bugünkü davranış birebir).
  | { kind: 'text'; text: string }
  | { kind: 'heading'; level: 1 | 2 | 3; text: string }
  | { kind: 'list'; ordered: boolean; items: string[] }
  // `open: true` = kapanış çiti HENÜZ gelmedi (akış sürüyor ya da
  // yanıt kesildi). Çizim tarafı kopyala butonunu buna göre saklıyor:
  // yarım kod kopyalanırsa operatör sessizce eksik komut çalıştırır.
  | { kind: 'code'; lang: string; code: string; open: boolean }
  | { kind: 'table'; head: string[]; align: ChatBlockAlign[]; rows: string[][] };

const FENCE = /^\s{0,3}```/;
const HEADING = /^(#{1,6})\s+(.*)$/;
const BULLET = /^\s{0,3}[-*+]\s+(.*)$/;
const ORDERED = /^\s{0,3}\d{1,3}[.)]\s+(.*)$/;

// Kısmi satırda "bitmemiş BLOK İŞARETİ" kalıpları. Bu üçü tutulur
// (ekrana basılmaz), gerisi basılır — gerekçe parseChatBlocks'ta.
const PARTIAL_FENCE = /^\s{0,3}`{1,3}[a-z0-9_+-]*$/i;
const PARTIAL_ROW = /^\s{0,3}\|/;
const PARTIAL_HEADING = /^\s{0,3}#{1,6}\s*$/;

/** Satır bir tablo satırı gibi mi duruyor: `|` ile BAŞLIYOR mu. */
function isRowLine(line: string): boolean {
  return line.trim().startsWith('|');
}

// Ayraç satırı: `|---|---:|`. Kural GFM'in kendisi — hücrelerin HEPSİ
// tire (istenirse iki yanında hizalama iki noktası) ve hücre SAYISI
// başlıkla aynı.
//
// Sayı eşitliği asıl kapı: onsuz `| önemli | not |` satırının altındaki
// bir `---` yatay çizgisi ayraç sanılır ve düzyazı boş bir tabloya
// dönüşür. Baştaki çubuğu ŞART koşmuyoruz (model bazen `---|---:`
// yazıyor) — yanlış-pozitife karşı asıl çapa BAŞLIK satırının çubukla
// başlaması (isRowLine), yani "hızlı | yavaş" gibi bir cümle hiçbir
// hâlde tablo başlatmıyor.
function isDelimRow(line: string, ncols: number): boolean {
  const s = line.trim();
  if (!/-/.test(s)) return false;
  if (!/^[|\s:-]+$/.test(s)) return false;
  const cells = splitRow(s);
  return cells.length === ncols && cells.every(c => /^:?-+:?$/.test(c));
}

// splitRow — bir tablo satırını hücrelere böler. İki tuzağı biliyor:
//   `\|`      → kaçırılmış çubuk, hücre İÇERİĞİ (ayırıcı değil).
//   `` `a|b` `` → kod aralığındaki çubuk bölmez; aksi hâlde model
//              `p95 | p99` gibi bir kod parçası yazdığında kolonlar
//              kayardı.
export function splitRow(line: string): string[] {
  let s = line.trim();
  if (s.startsWith('|')) s = s.slice(1);
  if (s.endsWith('|') && !s.endsWith('\\|')) s = s.slice(0, -1);
  const cells: string[] = [];
  let cur = '';
  let inCode = false;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (c === '\\' && s[i + 1] === '|') { cur += '|'; i++; continue; }
    if (c === '`') { inCode = !inCode; cur += c; continue; }
    if (c === '|' && !inCode) { cells.push(cur.trim()); cur = ''; continue; }
    cur += c;
  }
  cells.push(cur.trim());
  return cells;
}

function alignOf(cell: string): ChatBlockAlign {
  const s = cell.trim();
  const l = s.startsWith(':');
  const r = s.endsWith(':');
  if (l && r) return 'center';
  if (r) return 'right';
  return 'left';
}

/** Metin koşusunun kenarındaki boş satırları atar (blok arası çift boşluk olmasın). */
function trimRun(lines: string[]): string {
  let a = 0;
  let b = lines.length;
  while (a < b && lines[a].trim() === '') a++;
  while (b > a && lines[b - 1].trim() === '') b--;
  return lines.slice(a, b).join('\n');
}

/**
 * parseChatBlocks — asistan metnini bloklara böler.
 *
 * `streaming` = tur hâlâ akıyor (turn.pending). AKIŞ KARARI, tek
 * cümleyle: son satır `\n` ile bitmediyse o satır YARIM'dır ve yalnız
 * "bitmemiş bir blok işareti" ise TUTULUR (bir sonraki delta'da tam
 * hâliyle gelir); değilse normal basılır.
 *
 * Neden bu ikili ayrım — iki başarısızlık kipi var ve ikisi de kötü:
 *   (a) her yarım satırı tutmak: düzyazı bir cevap dakikalarca TEK
 *       satır olabiliyor (model `\n` basmadan yazıyor). Tutarsak balon
 *       akış boyunca BOŞ durur — "AI takıldı" görünür. Kabul edilemez.
 *   (b) hiçbirini tutmamak: `| Servis | p9` satırı ham çubuklarla bir
 *       an görünür, sonra tabloya "sıçrar"; `` ``` `` çiti yalnız
 *       backtick olarak yanıp söner. Bugünkü kusurun ta kendisi.
 * Ayrım şu ölçüte oturuyor: karakterler ekranda BİLGİ mi taşıyor
 * (düzyazı → bas), yoksa henüz YAPI mı kuruyor (çubuk/çit/diyez →
 * tut)? Tutulan en fazla bir satırdır ve balonun imleci (.cm-ai-cursor)
 * zaten "yazıyor" sinyalini veriyor, yani duraklama gibi okunmuyor.
 *
 * `streaming: false` (akış bitti) hiçbir şeyi tutmaz: kapanmamış bir
 * fence bile `open: true` bir kod bloğu olarak çizilir. Kesilmiş bir
 * cevapta içeriği YUTMAK, ham basmaktan daha kötü (Markdown.tsx'in
 * "bilinmeyen işaret olduğu gibi geçer" ilkesi).
 */
export function parseChatBlocks(text: string, streaming = false): ChatBlock[] {
  const blocks: ChatBlock[] = [];
  if (!text) return blocks;
  const lines = text.split('\n');
  // Yarım satırın indeksi (yoksa -1). `\n` ile biten metinde son öğe
  // boş dizedir, yani yarım satır YOKTUR.
  const partial = streaming && !text.endsWith('\n') ? lines.length - 1 : -1;
  const full = (i: number) => i < lines.length && i !== partial;

  let run: string[] = [];
  const flushRun = () => {
    if (run.length === 0) return;
    const joined = trimRun(run);
    run = [];
    if (joined !== '') blocks.push({ kind: 'text', text: joined });
  };

  let i = 0;
  while (i < lines.length) {
    const line = lines[i];

    // ── Yarım satır: işaret kalıplarından biriyse TUT, değilse aşağıya düş.
    if (i === partial) {
      if (PARTIAL_FENCE.test(line) || PARTIAL_ROW.test(line) || PARTIAL_HEADING.test(line)) break;
    }

    // ── Fence (```lang … ```)
    if (FENCE.test(line)) {
      flushRun();
      const lang = line.trim().slice(3).trim().split(/\s+/)[0].toLowerCase();
      const code: string[] = [];
      let open = true;
      let j = i + 1;
      while (j < lines.length) {
        if (j === partial) {
          // Kapanış çiti yazılıyor olabilir → tut. Değilse yazılmakta
          // olan KOD satırıdır: kod içinde harf harf büyüme doğal, bas.
          if (!/^\s{0,3}`/.test(lines[j])) code.push(lines[j]);
          // j'yi SONA taşımak ŞART: yarım satır zaten son satırdır ve
          // `i = j` ile dış döngüye dönerse aynı satır İKİNCİ kez, bu
          // sefer düz metin olarak basılır (kod bloğunun altında ham
          // JSON/SQL olarak görünüyordu — bu satır olmadan test kızarır).
          j = lines.length;
          break;
        }
        if (FENCE.test(lines[j])) { open = false; j++; break; }
        code.push(lines[j]);
        j++;
      }
      // KAPANMAMIŞ blokta sondaki boş satırlar atılır: metin `\n` ile
      // bittiğinde split son öğe olarak boş dize verir ve `<pre>` onu
      // gerçek bir boş satır olarak çizer — kod bloğu her yeni satırda
      // bir satır boyu ZIPLAR. Kapanmış blokta gövde AYNEN korunuyor
      // (çitler arasındaki boşluk modelin/üreticinin kararı).
      while (open && code.length > 0 && code[code.length - 1].trim() === '') code.pop();
      blocks.push({ kind: 'code', lang, code: code.join('\n'), open });
      i = j;
      continue;
    }

    // ── Tablo: `|…` başlığı + hemen ardından TAM bir ayraç satırı.
    const head = isRowLine(line) ? splitRow(line) : null;
    if (head && full(i + 1) && isDelimRow(lines[i + 1], head.length)) {
      flushRun();
      const align = splitRow(lines[i + 1]).map(alignOf);
      const rows: string[][] = [];
      let j = i + 2;
      // Yarım satır burada da TUTULUYOR: yarım bir satırı satır olarak
      // basmak kolon genişliklerini her delta'da zıplatır.
      while (full(j) && isRowLine(lines[j])) {
        const cells = splitRow(lines[j]);
        // Kısa satır doldurulur; FAZLA hücre KORUNUR (içerik yutmayız —
        // tablo bir kolon geniş çizilir, o kolonun başlığı boş kalır).
        while (cells.length < head.length) cells.push('');
        rows.push(cells);
        j++;
      }
      blocks.push({ kind: 'table', head, align, rows });
      i = j;
      continue;
    }

    // ── Başlık (#, ##, ###). #### ve ötesi 3'e KIRPILIR: model derin
    // başlık üretiyor ve balonun içinde 4. kademe görsel olarak yok.
    const h = line.match(HEADING);
    if (h) {
      flushRun();
      const level = Math.min(h[1].length, 3) as 1 | 2 | 3;
      blocks.push({ kind: 'heading', level, text: h[2].trim() });
      i++;
      continue;
    }

    // ── Liste (- / * / + ve 1. / 1) ). Tür değişimi listeyi kapatır.
    const b = line.match(BULLET);
    const o = b ? null : line.match(ORDERED);
    if (b || o) {
      flushRun();
      const ordered = !!o;
      const items: string[] = [b ? b[1] : (o as RegExpMatchArray)[1]];
      let j = i + 1;
      while (j < lines.length) {
        if (j === partial && (PARTIAL_FENCE.test(lines[j]) || PARTIAL_ROW.test(lines[j]) || PARTIAL_HEADING.test(lines[j]))) break;
        const nb = lines[j].match(BULLET);
        const no = nb ? null : lines[j].match(ORDERED);
        if (ordered ? !no : !nb) break;
        items.push(nb ? nb[1] : (no as RegExpMatchArray)[1]);
        j++;
      }
      blocks.push({ kind: 'list', ordered, items });
      i = j;
      continue;
    }

    run.push(line);
    i++;
  }
  flushRun();
  return blocks;
}
