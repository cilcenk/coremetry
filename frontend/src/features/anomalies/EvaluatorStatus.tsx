// v0.9.550 — evaluator durum şeridi.
//
// Operatör raporu: "worker modda çalışan evaluator gerçekten problem
// bulsun ve Problems sekmesinde göreyim, bazen sanki takıldığını
// hissediyorum."
//
// Bu bileşenin tek işi o hissi ekranda cevaplamak. Öncesinde sayfa
// boşken "No open alerts — all clear!" diyor ve altına
// "The evaluator runs once per minute" yazıyordu — İKİNCİSİ
// ÖLÇÜLMEMİŞ BİR İDDİAYDI. Evaluator ölü olsa sayfa yine aynı
// cümleyi kurardı, üstelik ✓ ikonuyla. Yani sistem sessizken en
// güven verici hâlini gösteriyordu.
//
// Tasarım kuralı: "bilmiyoruz" ASLA yeşil değildir. unknown hâli
// nötr/uyarı tonunda çıkar, çünkü ölçemediğimizi iyi habere çevirmek
// düzeltmeye çalıştığımız hatanın ta kendisi.
import { useEvaluatorHealth } from '@/lib/queries';
import type { EvaluatorHealth } from '@/lib/types';

// tone — durumdan rozet sınıfına. Paylaşılan .badge tokenları
// kullanılır (b-ok/b-warn/b-err/b-gray); elle renk yazılmaz, tema
// değişince kendiliğinden doğru kalsın.
function tone(status: EvaluatorHealth['status']): string {
  switch (status) {
    case 'ok':      return 'b-ok';
    case 'stale':   return 'b-err';   // takılma = kırmızı: alarm üretimi durmuş olabilir
    case 'failing': return 'b-warn';
    default:        return 'b-gray';  // unknown — asla yeşil
  }
}

function label(status: EvaluatorHealth['status']): string {
  switch (status) {
    case 'ok':      return 'Evaluator çalışıyor';
    case 'stale':   return 'Evaluator takılmış olabilir';
    case 'failing': return 'Evaluator hata veriyor';
    default:        return 'Evaluator durumu bilinmiyor';
  }
}

/**
 * Problems sayfasındaki tek satırlık evaluator durum şeridi.
 *
 * `compact` — boş-durum kutusunun İÇİNDE kullanılırken kenarlık/arka
 * plan taşımaz (kutu zaten bir çerçeve; iç içe iki çerçeve gürültü).
 */
export default function EvaluatorStatus({ compact = false }: { compact?: boolean }) {
  const { data } = useEvaluatorHealth();

  // Veri henüz yokken hiçbir şey çizme. Yükleniyorken "bilinmiyor"
  // göstermek, iki saniyede bir yanıp sönen sahte bir uyarı olurdu.
  if (!data) return null;

  const detail =
    data.status === 'ok' && data.durationMs > 0
      ? ` · son tik ${data.durationMs} ms`
      : '';

  return (
    <div
      style={{
        display: 'flex', alignItems: 'center', gap: 8,
        fontSize: 12, color: 'var(--text2)',
        padding: compact ? 0 : '6px 10px',
        marginTop: compact ? 8 : 0,
        border: compact ? 'none' : '1px solid var(--border)',
        borderRadius: compact ? 0 : 6,
        background: compact ? 'transparent' : 'var(--bg2)',
      }}
    >
      <span className={`badge ${tone(data.status)}`}>{label(data.status)}</span>
      <span>{data.reason}{detail}</span>
    </div>
  );
}
