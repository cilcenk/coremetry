// pages/explore/RowsCappedNote.tsx — satır tavanı şeridi, GRAFİKSİZ
// görünümler için (v0.9.809).
//
// v0.9.458 şeridi QueryPanel'in başlık satırına yazıyordu ve orada kaldı:
// yalnız ÇİZGİ AİLESİ (line/area/bars/stacked) QueryPanel çiziyor.
// viz='table' hiç panel çizmiyor (GroupTable görünümün kendisi),
// stat/toplist/pie ise SummaryViz'e gidiyor — üçünde de "50k satır tavanı
// doldu, liste eksik olabilir" uyarısı HİÇBİR YERDE görünmüyordu. Aynı
// veri, aynı yalan, ama operatör tablo modundayken uyarısız.
//
// Şerit AYNI cümleyi kurar (iki metin, iki bakım yeri olmasın diye
// QueryPanel'in title'ı ile aynı gerekçeyi anlatır) ve hangi harflerin
// kırpıldığını sayar — tabloda satırlar harflerden geldiği için "hangi
// sorgu eksik" sorusunun cevabı gerekli.

import type { PanelData } from './PanelStack';

// cappedLetters — tavana çarpan harfler, alfabetik. Saf; tablo-testli.
export function cappedLetters(panels: PanelData[]): string[] {
  return panels
    .filter(p => p.rowsCapped)
    .map(p => p.letter)
    .sort();
}

export const ROWS_CAPPED_HINT =
  'Sorgu 50k satır tavanına çarptı — seriler grup anahtarına göre ALFABETİK '
  + 'kesildi; geç harfli seriler eksik olabilir ve "daha" sayısı gerçek evreni '
  + 'bilemez. Pencereyi daralt, adımı büyüt ya da filtre ekle.';

export function RowsCappedNote({ panels }: { panels: PanelData[] }) {
  const letters = cappedLetters(panels);
  if (letters.length === 0) return null;
  return (
    <div role="status" style={{
      display: 'flex', alignItems: 'flex-start', gap: 6,
      fontSize: 11, color: 'var(--warn)', marginBottom: 8,
    }} title={ROWS_CAPPED_HINT}>
      <span aria-hidden>⚠</span>
      <span>
        Satır tavanı doldu (sorgu {letters.join(', ')}) — liste eksik olabilir.
        {' '}
        <span style={{ color: 'var(--text2)' }}>
          Pencereyi daralt, adımı büyüt ya da filtre ekle.
        </span>
      </span>
    </div>
  );
}
