// resetLayoutAdoption — v0.9.1332. Operatör raporu: "Neden sayfa yatayda
// kayıyor" (/inbox, prod 1328).
//
// TEŞHİS: kayan şey sayfa gövdesi DEĞİL — tablo kendi `.table-wrap`
// kabında (`overflow-x: auto`, globals.css:1157). Operatörün kendi ekran
// görüntüsü bunu ayırt ediyordu: filtre şeridi sol kenarda durup tablo
// başlığı sağa kaymıştı (`PRIORITY` → `RIT...`). Sayfa kaysaydı şerit de
// kayardı.
//
// AMA sebebin İKİ katmanı var ve ikincisinin çıkış yolu yoktu:
//   1. Beyan edilen bütçe — /inbox'ta sabit kolonlar 984px
//      (34+80+100+190+110+150+170+150) ve tek esnek kolon `detail`.
//   2. SÜRÜKLENMİŞ genişlikler — `localStorage`'da `dt.<key>.widths`.
//      `columnLayoutSig` (lib/dataTable.ts:260) bu haritayı yalnız BEYAN
//      EDİLEN bir genişlik değişince atıyor; saf sürükleme sonsuza kadar
//      yaşıyor. Tabloyu ekrandan taşıran bir sürükleme, geri dönüşü
//      olmayan bir çıkmaz oluyor. v0.9.660'ta Users tablosunda ölçülen
//      "ikinci katman" tam buydu.
//
// ResetLayoutButton v0.9.660'ta tam bu iş için yazıldı — ve ÖLÇÜLDÜ:
// 75 dosya `useDataTable` kullanıyor, o gün yalnız 1'i (Users) butonu
// bağlıyordu. Yani çıkmaz ~74 tabloda yaşıyordu ve kimse fark etmedi
// çünkü buton VARDI, sadece hiçbir yerde render edilmiyordu.
//
// BU TEST NE YAPIYOR: boşluğu SESLİ tutuyor. Süpürme (kalan ~73 tablo)
// bu sürümün kapsamı değil — operatör tek sayfa bildirdi ve 73 dosyaya
// dokunan bir dilim onun raporunun cevabı olmaz. Ama sessiz kırpma bu
// depoda yasak: sayı burada yazılı, yeni bir adoptasyon eklendiğinde bu
// test onu görecek ve rakamı güncellemeye zorlayacak. Rakam düştükçe
// süpürme ilerliyor demektir; ARTARSA yeni bir çıkmaz eklenmiş demektir.
//
// ⚠ NEDEN ÇİFT TIK YETMİYOR: `DataTable.tsx:289` tutamağa çift tıkta
// `resetLayout()` çağırıyor ve bu gerçek bir kaçış yolu — ama hiçbir
// yerde YAZMIYOR. Keşfedilmeyen bir affordance, olmayan bir affordance.
import { describe, it, expect } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, join } from 'node:path';

const SRC = resolve(__dirname, '..');

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const p = join(dir, e);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (p.endsWith('.tsx')) out.push(p);
  }
  return out;
}

// Testleri ve primitifin KENDİSİNİ sayma: DataTable.tsx butonu tanımlıyor,
// tüketmiyor.
const PRIMITIVE = join('components', 'DataTable.tsx');
function isConsumer(abs: string): boolean {
  return !abs.endsWith('.test.tsx') && !abs.includes('.test.') && !abs.endsWith(PRIMITIVE);
}

function consumers(): { uses: string[]; binds: string[] } {
  const uses: string[] = [];
  const binds: string[] = [];
  for (const abs of walk(SRC)) {
    if (!isConsumer(abs)) continue;
    const src = readFileSync(abs, 'utf8');
    const rel = abs.slice(SRC.length + 1);
    if (src.includes('useDataTable')) uses.push(rel);
    if (src.includes('<ResetLayoutButton')) binds.push(rel);
  }
  return { uses, binds };
}

describe('ResetLayoutButton adoption', () => {
  it('operatörün bildirdiği sayfa (/inbox) ve emsal (Users) butonu BAĞLIYOR', () => {
    const { binds } = consumers();
    // Bu iki satır gerileyemez: /inbox operatör raporunun konusu,
    // Users v0.9.660'ta aynı çıkmazın ölçüldüğü yer.
    expect(binds).toContain(join('pages', 'Inbox.tsx'));
    expect(binds).toContain(join('pages', 'Users.tsx'));
  });

  it('kalan boşluk SESLİ — süpürülmemiş tablo sayısı kayıtlı', () => {
    const { uses, binds } = consumers();
    const gap = uses.length - binds.length;
    // v0.9.1332 ölçümü: 75 kullanıcı / 2 bağlayan → 73 açık.
    //
    // Bu rakam bir HEDEF değil, bir MUHASEBE. Düştüyse süpürme ilerledi:
    // sayıyı güncelle. ARTTIYSA yeni bir tabloya geri dönüşü olmayan bir
    // sürükleme çıkmazı eklenmiş: butonu bağla, sayıyı güncelleme.
    expect(gap).toBeLessThanOrEqual(73);
    // Sağlık kontrolü: yürüyüş gerçekten dosya buluyor. Sıfır dönen bir
    // tarayıcı bu testi sessizce yeşil yapardı (boş küme tuzağı).
    expect(uses.length).toBeGreaterThan(50);
  });
});
