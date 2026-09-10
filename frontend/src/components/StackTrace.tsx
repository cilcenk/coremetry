import { Fragment, useMemo } from 'react';
import type { StackFrameLink } from '@/lib/types';

// StackTrace — v0.10.581 (Aşama 1.5): exception stack trace'inin
// TIKLANABİLİR hâli.
//
// SAF ÇİZİM. Bu dosyada ne fetch var ne ayrıştırma. Frame künyesi
// sunucudan gelir (kanonik parser Go'da: `internal/stackparse`) ve
// burada YALNIZ süsleme yapılır. TS tarafında ikinci bir frame
// regex'i açmak, iki dilde iki ayrı "doğru frame" demekti —
// `lib/codeQuote.ts`teki desen AI markdown'ına aittir ve bu yüzeyde
// KULLANILMAZ.
//
// SÜSLEME SATIR İNDEKSİNE GÖRE, metin eşleştirmesine göre DEĞİL.
// Aynı frame bir stack'te birden çok kez geçebilir ("Caused by:"
// zincirleri, özyineleme, `... 42 more` katlamaları); metinden
// eşleştirme hepsini birden boyar ve sunucunun yalnız BİRİ için
// ürettiği URL'yi öbürlerine de yapıştırırdı. `lineIndex` sunucunun
// GÖRDÜĞÜ metnin indeksi olduğu için çağıran, gönderdiği dizgenin
// AYNISINI çizmek zorunda (SpanDetail ikisine de `formatStack`
// çıktısını veriyor).
//
// METİN AYNEN KORUNUR. Satırların içine `↗` gibi bir dış-link glifi
// EKLENMİYOR — depodaki öbür dış linklerden bilinçli sapma. Sebep:
// burası monospace bir stack dökümü ve operatör onu seçip kopyalıyor;
// araya sıkıştırılan bir glif, kopyalanan metni sahte kılar. Dış
// bağlantı işareti olarak uygulamanın kendi link anatomisi (mavi +
// hover altçizgi) ve `title` yeterli.
//
// DEGRADASYON SESSİZ: `frames` yoksa (uç `configured:false` döndü,
// istek düştü, ya da hiçbir frame tanınmadı) çıktı bugünkü düz metnin
// BİREBİR aynısı olur. Bu yüzeyde hata/uyarı gösterilmez.
export function StackTrace({ stack, frames, warning, verified }: {
  /** Çizilecek metin. Sunucuya gönderilen dizgenin AYNISI olmalı. */
  stack: string;
  /** Sunucudan gelen frame künyesi. Yoksa düz metne düşülür. */
  frames?: StackFrameLink[];
  /** Sürüm uyarısı; YALNIZ gerçekten link üretildiyse ve bir kez. */
  warning?: string;
  /** v0.10.590 — sürüm VCS'te doğrulandıysa uyarı yerine onay tonu. */
  verified?: boolean;
}) {
  // İlk kazanır: sunucu aynı satır için birden çok frame dönerse
  // (bozuk bir gramer, iç içe geçmiş dil), satır TEK bir süs alır —
  // aksi hâlde çizim sırası cevabın sırasına bağlı olurdu.
  const byLine = useMemo(() => {
    const m = new Map<number, StackFrameLink>();
    for (const f of frames ?? []) {
      if (f.lineIndex >= 0 && !m.has(f.lineIndex)) m.set(f.lineIndex, f);
    }
    return m;
  }, [frames]);

  // Uyarı, LİNK VARSA anlamlı: "baktığın dosya o sürüm olmayabilir"
  // cümlesinin muhatabı, tıklayacak olan operatör. Tek bir link bile
  // üretilmediyse uyarı gürültüdür.
  const hasLink = useMemo(
    () => (frames ?? []).some(f => f.isApp && !!f.url),
    [frames],
  );

  // Süslenecek hiçbir şey yok → BUGÜNKÜ görünüm, birebir. Satır satır
  // çizmiyoruz bile: tek bir metin düğümü, yani boşluk/sekme davranışı
  // tartışmasız aynı kalıyor.
  if (byLine.size === 0) return <pre className="ex-stack">{stack}</pre>;

  const lines = stack.split('\n');

  return (
    <>
      {hasLink && !!warning && (
        <div className="ex-stack-note">
          <span className={verified ? "badge b-ok" : "badge b-warn"}>{warning}</span>
        </div>
      )}
      <pre className="ex-stack">
        {lines.map((line, i) => (
          <Fragment key={i}>
            {frameLine(line, byLine.get(i))}
            {/* Ayırıcı AYRI bir düğüm: satır içeriği <a>/<span> içine
                girse de `\n` dışarıda kalır, yani satır sayısı girdiyle
                aynı — hiçbir satır yutulmaz. */}
            {i < lines.length - 1 ? '\n' : ''}
          </Fragment>
        ))}
      </pre>
    </>
  );
}

// frameLine — TEK satırın süslenmiş hâli. Metin her dalda AYNEN
// korunur; değişen yalnız sarmalayıcı.
function frameLine(line: string, f: StackFrameLink | undefined) {
  // Tanınmayan satır (mesaj başlığı, "Caused by:", "... 42 more",
  // boş satır) hiç dokunulmadan geçer.
  if (!f) return line;

  // Kütüphane/framework frame'i: SOLUK. Operatörün aradığı satır
  // kendi kodu; 40 satırlık Spring/Netty gövdesi onun arka planı.
  if (!f.isApp) return <span className="ex-frame-lib">{line}</span>;

  // Uygulama frame'i ama URL YOK — depo çözülemedi, dosya ağaçta
  // bulunamadı, DevOps ayarlı değil. Link ÜRETİLMEZ (ölü bir link,
  // linksiz bir satırdan kötüdür); nedeni hover'da durur.
  if (!f.url) return <span className="ex-frame-app" title={f.reason || undefined}>{line}</span>;

  return (
    <a className="ex-frame-link" href={f.url}
       target="_blank" rel="noreferrer"
       title={`DevOps'ta aç: ${f.file}${f.line > 0 ? `:${f.line}` : ''}`}>
      {line}
    </a>
  );
}
