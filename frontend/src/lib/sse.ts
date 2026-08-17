import type { AIStreamFrame } from './types';

// sse.ts — sunucunun `event:`/`data:` çerçevelerini okuyan TEK ayrıştırıcı
// (v0.9.1127, AI Faz 1.5).
//
// Bu okuyucu v0.6.53'ten beri api.ts'in içinde, copilotChat'in gövdesine
// gömülü yaşıyordu. Faz 1.5 ikinci bir tüketici getirdi (tek-atış ✨
// Explain'in akan varyantı) ve gömülü bir ayrıştırıcının ikinci kopyası,
// çerçeve şekli değiştiğinde İKİ yerde düzeltilmesi gereken bir hata
// sınıfı demekti. Tek yazılış: sohbet ve explain aynı bayt akışını aynı
// kodla çözüyor.
//
// EventSource KULLANILMIYOR ve bu bilinçli: her iki yüzey de POST gövdesi
// gönderiyor (mesaj geçmişi / prompt seçenekleri), EventSource ise yalnız
// GET yapar. Elle çözümleme bu yüzden var — süs değil.

/** SSE gövdesinin bir çerçevesi. `kind` = `event:` satırı. */
export type { AIStreamFrame };

/**
 * readSSE — bir `Response` gövdesini çerçevelere ayırır ve her birini
 * `onFrame`'e verir. Akış kapandığında (ya da `done` çerçevesinden sonra
 * sunucu kapattığında) promise çözülür.
 *
 * Çerçeve sınırı BOŞ SATIR (`\n\n`) — tek bir `read()` bir çerçevenin
 * yarısını, ya da üç çerçeveyi birden taşıyabilir; tampon iki durumu da
 * kaldırır. Bozuk JSON taşıyan çerçeve SESSİZCE atlanır: bir modelin
 * ürettiği tek kötü çerçeve, o ana kadar akmış cevabı çöpe atmamalı.
 *
 * JENERİK, çünkü GÜVEN SINIRI burası: `JSON.parse` gerçekten bilinmeyen
 * bir şekil döndürüyor ve o şekli daraltan tek bir dönüşüm olmalı.
 * Sohbet yüzeyi `readSSE<ChatStreamEvent>` diyerek dar birleşimini alır,
 * explain yüzeyi varsayılan açık şekli kullanır — hiçbir çağıranda ikinci
 * bir dönüşüm gerekmez.
 */
export async function readSSE<T extends { kind: string } = AIStreamFrame>(
  body: ReadableStream<Uint8Array>,
  onFrame: (f: T) => void,
): Promise<void> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buf = '';
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let sep: number;
    while ((sep = buf.indexOf('\n\n')) !== -1) {
      const frame = buf.slice(0, sep);
      buf = buf.slice(sep + 2);
      let event = 'message';
      let data = '';
      for (const line of frame.split('\n')) {
        if (line.startsWith('event:')) event = line.slice(6).trim();
        else if (line.startsWith('data:')) data += line.slice(5).trim();
      }
      if (!data) continue;
      try {
        const payload = JSON.parse(data);
        onFrame({ kind: event, ...payload } as T);
      } catch { /* bozuk çerçeve — akışı kesme */ }
    }
  }
}
