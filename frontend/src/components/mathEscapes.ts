// mathEscapes — LLM'in bastığı LaTeX kaçışlarını okunur karaktere çevirir
// (v0.10.12, operatör bildirimi).
//
// Belirti: "Explain root cause" panelinde akış zinciri şöyle görünüyordu:
//
//     servis-a $\rightarrow$ servis-b $\rightarrow$ servis-c
//
// Yani ekranda ok yerine LaTeX kaynağı. Operatörün okuduğu tek satır —
// hatanın hangi servisten hangisine yayıldığı — gürültüye gömülüyordu.
//
// ── NEDEN RENDERER TARAFINDA ────────────────────────────────────────────
//
// Prompt'a "LaTeX kullanma" yazmak akla ilk gelen çözüm ve YETMEZ: bu
// kurulumda model yerel ve küçük, ve küçük modeller biçim talimatlarını
// güvenilir biçimde izlemiyor (hafıza: "küçük-model: prefetch+narrate >
// tool-loop"). Renderer tarafı ise modelden BAĞIMSIZ çalışır — bugün
// gemma, yarın başka bir model, aynı davranış.
//
// Prompt'a bir satır eklemek yine de zararsız ve gürültüyü kaynağında
// azaltır; ama TEK BAŞINA düzeltme sayılamaz, o yüzden bu dosya var.
//
// ── NEDEN GENEL BİR LaTeX AYRIŞTIRICI DEĞİL ─────────────────────────────
//
// Kapsam KASITLI olarak dar: bilinen komut listesi. `$...$` içindeki her
// şeyi işlemek, metindeki dolar işaretlerini (fiyat, `$PATH`, shell
// örneği) bozardı — operatör metni bunları gerçekten içeriyor. Bilinen
// komut yoksa dizge AYNEN kalıyor: tanınmayan girdi sessizce
// değiştirilmez.

/** Küçük modellerin fiilen bastığı komutlar. Genişletmeden ÖNCE ölç. */
const COMMANDS: Record<string, string> = {
  rightarrow: '→', to: '→', Rightarrow: '⇒',
  leftarrow: '←', gets: '←', Leftarrow: '⇐',
  leftrightarrow: '↔',
  times: '×', cdot: '·', div: '÷', pm: '±',
  approx: '≈', neq: '≠', ne: '≠',
  leq: '≤', le: '≤', geq: '≥', ge: '≥',
  ldots: '…', dots: '…',
  alpha: 'α', beta: 'β', delta: 'δ', Delta: 'Δ', sigma: 'σ', mu: 'μ',
  infty: '∞', pi: 'π',
};

// Uzun adlar ÖNCE denenmeli: `\le` deseni `\leq`in başını yiyebilir ve
// geriye bir `q` bırakır. Sıralama bu yüzden uzunluğa göre.
const NAMES = Object.keys(COMMANDS).sort((a, b) => b.length - a.length);
const CMD_RE = new RegExp(`\\\\(${NAMES.join('|')})(?![a-zA-Z])`, 'g');

// `$\cmd$` ya da `$ \cmd $` — tek komutu saran dolarlar. Yalnız BU şekil
// soyuluyor; `$5 \times 3$` gibi karışık içerikte dolarlar kalır ve
// yalnız komut çevrilir (yarım iş, ama yanlış iş değil).
const WRAPPED_RE = new RegExp(`\\$\\s*\\\\(${NAMES.join('|')})(?![a-zA-Z])\\s*\\$`, 'g');

/**
 * normalizeMathEscapes — LaTeX kaçışlarını unicode'a çevirir.
 *
 * Saf: LLM çıktısı deterministik değil, o yüzden düzeltmenin kendisi
 * deterministik ve tablo-testli olmalı.
 */
export function normalizeMathEscapes(s: string): string {
  if (!s.includes('\\')) return s;
  // Önce sarmalı hâl (dolarlar da gitsin), sonra çıplak komutlar.
  return s
    .replace(WRAPPED_RE, (_m, name: string) => COMMANDS[name])
    .replace(CMD_RE, (_m, name: string) => COMMANDS[name]);
}
