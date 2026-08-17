import { aiSubjectQuestion } from '@/components/ai/drawerChat';
import type { InsightKind, InsightResponse } from './types';

// insightCard — gömülü insight kartının SAF yardımcıları (v0.9.1130,
// AI Faz 2.2; docs/plans/ai-assistant-design-2026-08-16.md §2.3).
//
// Neden ayrı modül: üçü de React'ten bağımsız karar ve üçü de sessizce
// yanlış olabilir (yanlış RENK, yanlış NAVİGASYON, boş panel). Saf
// olunca table-test edilirler — emsal lib/aiSubject.ts, lib/logsUrl.ts.

/**
 * insightTone — Signal.Severity → paylaşılan `.badge` tonu.
 *
 * `''` GEÇERLİ bir değer ve "nötr bilgi" demek (contract.go: servis adı,
 * operasyon sayısı). BİLİNMEYEN bir şiddet de nötre düşer: sunucu bir gün
 * dördüncü bir değer eklerse kart onu yanlış renkle basmaz — hiç renk
 * basmaz. Yanlış renk, renksizlikten kötü: `err` tonuyla çizilmiş bir
 * "ok" satırı operatörü olmayan bir olaya koşturur.
 */
export function insightTone(severity?: string): 'b-ok' | 'b-warn' | 'b-err' | null {
  switch ((severity ?? '').trim().toLowerCase()) {
    case 'ok':   return 'b-ok';
    case 'warn': return 'b-warn';
    case 'err':  return 'b-err';
    default:     return null;
  }
}

/**
 * insightHrefInternal — sunucu-üretimi link uygulama İÇİ mi (react-router
 * `<Link>`) yoksa dışa mı (`<a>`) çizilecek?
 *
 * `//` BİLEREK dışarıda: `//evil.example/x` protokol-göreli bir MUTLAK
 * URL'dir ama tek `/` testini geçer — router'a verilirse uygulama sessizce
 * başka bir siteye gider. Bugünkü sunucu üreticisi (links.go) yalnız
 * `/logs`, `/traces`, `/service`, `/problems`, `/trace` basıyor; bu kapı o
 * yüzden 3 karakterlik bir sigortadır, bir ihtiyaç listesi değil — FE
 * href'i AYNEN çiziyor, dolayısıyla doğrulamayı da FE yapmalı.
 */
export function insightHrefInternal(href: string): boolean {
  return href.startsWith('/') && !href.startsWith('//');
}

/**
 * insightHasEvidence — kart çizmeye değer bir şey geldi mi?
 *
 * Boş bir panel çizmek yerine `<Empty/>` göstermenin kapısı. prose de
 * sayılıyor: sinyalsiz ama anlatısı olan bir cevap (sunucu projeksiyonu
 * hepsini elemiş, model yine bir şey demiş) hâlâ okunacak bir karttır.
 */
export function insightHasEvidence(r: InsightResponse | null): boolean {
  if (!r) return false;
  return r.signals.length > 0 || r.links.length > 0 || r.prose.trim() !== '';
}

/**
 * insightQuestion — kartın "💬 Chat'te devam et" köprüsünün seed
 * sorusu (v0.9.1137, Faz 2.4).
 *
 * 2.3'e kadar kart doğrudan `aiSubjectQuestion(kind, id)` çağırıyordu:
 * iki kart türü (`exception`/`problem`) AIKind'ın alt kümesiydi. 2.4'te
 * bu ilişki KIRILDI — `log-pattern` ve `slow-query` çekmece türü DEĞİL
 * (çekmecenin explain zinciri onları bilmiyor). aiSubjectQuestion'ın
 * `default` dalı bilinmeyen türü "Bu trace'i açıkla" yapıyor, yani
 * derleyici zorlamasa bir desen kartı sohbete TRACE sorusu yollardı:
 * sessiz, tip-doğru ve tamamen yanlış (CopilotExplain'in kind zincirinin
 * aynı tuzağı — InsightCard.tsx başındaki not).
 *
 * Bu yüzden switch TÜKETİCİ ve `default`SIZ: beşinci bir tür eklenirse
 * tsc kırılır, sessizce yanlış soru üretilmez.
 */
export function insightQuestion(kind: InsightKind, id: string): string {
  switch (kind) {
    case 'exception':
    case 'problem':
      // Bu ikisi AIKind ile ÖRTÜŞÜYOR — tek kaynak orada kalsın
      // (aynı özne için sohbet ve kart aynı soruyu sormalı).
      return aiSubjectQuestion(kind, id);
    case 'log-pattern':
      return `"${id}" log deseni bu pencerede neden arttı, ne yapmalıyım?`;
    case 'slow-query':
      // id bir hash — modele göstermenin değeri yok; soru sınıfın
      // KENDİSİNE dair (kanıt zaten explain bağlamında gidiyor).
      return 'Bu yavaş sorgu sınıfı neden yavaş, en yüksek etkili düzeltme ne?';
  }
}
