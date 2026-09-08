// DetailDrawer.msgops.pin.test.ts — v0.10.563 (Messaging Faz 4b).
//
// Neden dosya-metni testi: bölümün DOĞRU YERDE olması bir sözleşme ve
// hiçbir saf fonksiyon onu tutmuyor. Aynı sınıfın emsali
// DetailDrawer.kafka.pin.test.ts (v0.10.551) — orada da yerleşim
// operatör onayıyla sabitlenmişti ve tek bir JSX taşımasıyla sessizce
// kayabilirdi ("test edilmiş ama ulaşılamaz" sınıfı: hook yeşil, ekranda
// yok).
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const drawer = readFileSync(resolve(__dirname, 'DetailDrawer.tsx'), 'utf8');

describe('Operasyonlar · MV bölümü (v0.10.563)', () => {
  it('queue dalında: Consumers SONRASI, KafkaClientsSection ÖNCESİ', () => {
    const consumers = drawer.indexOf('title={`Consumers ·');
    const sec = drawer.indexOf('Operasyonlar · MV ·');
    const kafka = drawer.indexOf('<KafkaClientsSection');
    expect(consumers).toBeGreaterThan(-1);
    expect(sec).toBeGreaterThan(consumers);
    expect(sec).toBeLessThan(kafka);
  });

  it('tablo paylaşılan primitifi kullanıyor, storageKey deps-msg-ops', () => {
    expect(drawer).toContain("storageKey: 'deps-msg-ops'");
    expect(drawer).toContain('<DataTableColgroup dt={msgOpsDt} />');
    expect(drawer).toContain('<DataTableHead dt={msgOpsDt} />');
    // Varsayılan sıralama: en çok çağrılan operasyon üstte.
    expect(drawer).toContain("initialSort: { id: 'count', dir: 'desc' }");
    // v0.9.1332 çıkmaz sokağı: sürüklenen genişliğin geri dönüşü olsun.
    expect(drawer).toContain('<ResetLayoutButton dt={msgOpsDt} />');
  });

  it('hook erken dönüşlerden ÖNCE — rules-of-hooks (v0.9.873 tuzağı)', () => {
    const hook = drawer.indexOf("storageKey: 'deps-msg-ops'");
    const earlyReturn = drawer.indexOf('if (data === undefined) return <Spinner />;');
    expect(earlyReturn).toBeGreaterThan(-1);
    expect(hook).toBeLessThan(earlyReturn);
  });

  it("eksik operation '(yaymıyor)' diye yazılır, boş hücre bırakılmaz", () => {
    expect(drawer).toContain('opLabelTR(o.operation)');
    expect(drawer).toContain('OP_MISSING_TITLE');
    const helper = readFileSync(resolve(__dirname, 'msgOperations.ts'), 'utf8');
    expect(helper).toContain("'(yaymıyor)'");
    expect(helper).toContain('SDK messaging.operation.type/.name/.operation yaymıyor');
  });

  it('boş dilimde bölüm GİZLENMEZ — soluk satır yazar', () => {
    expect(drawer).toContain('Bu pencerede MV&#39;de operasyon satırı yok.');
    // <Empty> bloğu DEĞİL: bölüm başlığı ve satır sayısı görünür kalmalı.
    expect(drawer).not.toContain('Operasyon kırılımı yok');
  });

  it('operasyon TÜRÜ için trace pivotu YOK (span adı değil)', () => {
    // messagingTracesHref'in `operation` parametresi span ADINA çevriliyor;
    // buradaki değer operasyon TÜRÜ. Eşitleyen bir link ölü olurdu.
    const sec = drawer.indexOf('Operasyonlar · MV ·');
    const kafka = drawer.indexOf('<KafkaClientsSection');
    const block = drawer.slice(sec, kafka);
    expect(block).not.toContain('messagingTracesHref(');
  });
});
