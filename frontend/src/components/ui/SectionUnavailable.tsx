// SectionUnavailable — the one-line "this section has no answer for this
// window" note (v0.9.1364).
//
// NE YAPAR: bir detay bölümünün, komşularını boşaltmadan kendi yokluğunu
// yazmasını sağlar. Bu, endpoints/slowqueries çekmecelerinin NULL TOLERANCE
// sözleşmesinin görünen yüzü: bir bölümün payload'ı gelmediğinde o bölüm
// kendi satırını basar, kabuk çizilmeye devam eder
// (`pages/endpoints/detailSections.tsx:34-36`).
//
// NEDEN ATOM: PageShell'in ölçütü (`ui/PageShell.tsx:1-8`) — "bu atom bir
// soyutlama DEĞİL, tekrarlanan bir sabitin tek kaynağa çekilmesi; çıktısı
// bit bit aynı". Burada da öyle: iki nüsha (`endpoints/detailSections.tsx:57`
// ve `slowqueries/StmtDetailDrawer.tsx:161`) v0.9.1364'te BAYT BAYT aynıydı
// — ölçüldü, tilde yok. Yani bu terfi bir tasarım kararı değil, bir kopyanın
// silinmesi; render çıktısı değişmiyor.
//
// NEDEN `Empty` DEĞİL: `components/Spinner.tsx`in `Empty`si ikon + başlık +
// gövde taşıyan bir BLOK — bir kartın TAMAMI boşken doğru olan şey. Bu ise
// bir kartın İÇİNDEKİ tek bölümün tek satırlık dipnotu; ikisi aynı anda aynı
// kartta yaşayabilir ve karıştırılırlarsa "bu kart boş" ile "bu kartın bir
// hücresi ölçülemedi" ayrımı kaybolur.
//
// KAPI: `sectionAtomsGate.test.ts` — bu bileşenin İKİNCİ bir yerel tanımı
// (hangi yazımla olursa olsun) testi kırar. Kopya ailelerinin (Stat ×8,
// Field ×7) doğum mekanizması tam olarak "barrel'a bakan onu bulamadı"
// olduğu için atom barrel'a da giriyor.
// GEOMETRİ TOKEN'I, ÇIKTI AYNI: iki nüsha da ham `fontSize: 11` yazıyordu;
// `components/ui/` içindeki atomlar `geometryTokens.test.ts` (v0.9.909)
// gereği merdiveni token'la yazar. `--fs-xs` TAM OLARAK 11px
// (`globals.css:136`) ve hiçbir tema/yoğunluk onu yeniden tanımlamıyor —
// ölçüldü — yani terfi hâlâ bit bit aynı çıktıyı veriyor.
export function SectionUnavailable({ what }: { what: string }) {
  return (
    <div style={{ fontSize: 'var(--fs-xs)', color: 'var(--text3)' }}>
      {what} unavailable for this window.
    </div>
  );
}
