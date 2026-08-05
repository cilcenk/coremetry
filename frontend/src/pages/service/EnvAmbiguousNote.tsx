// v0.9.679 — ORTAM AYRIŞTIRILAMADI uyarısı.
//
// Operatörün SQL çıktısı: metric_points'te servis adları EKSİZ
// (bsa-chequenotes-notespayment), Coremetry'nin servis listesi ise
// trace'ten gelen EKLİ adı gösteriyor (...-uat). Eşleşme ancak eksiz
// adla kurulabiliyor.
//
// Bedeli: "bsa-deposit-uat" ve "bsa-deposit-prod" ikisi de
// "bsa-deposit"e iniyor. Sunucu önce ortamla kısıtlamayı deniyor
// (deployment.environment[.name]); tutmazsa kısıtsız eşleşiyor ve
// envAmbiguous işaretliyor.
//
// BU UYARI ŞART: sayı makul göründüğü için kimse fark etmez. Yanlış
// ortamın trafiğini doğru sanmak, hiç veri görmemekten kötüdür.
export function EnvAmbiguousNote() {
  return (
    <div style={{
      marginTop: 6, padding: '6px 8px', borderRadius: 4,
      background: 'var(--bg2)', border: '1px solid var(--border)',
      fontSize: 11, lineHeight: 1.5, color: 'var(--text3)',
    }}>
      ⚠ <b>Ortam ayrıştırılamadı.</b> Metrik tarafında servis adı ortam
      eki taşımıyor ve <code>deployment.environment</code> özniteliği de
      bulunamadı — bu seri birden çok ortamın (uat/prod/…) verisini
      taşıyor olabilir.
    </div>
  );
}
