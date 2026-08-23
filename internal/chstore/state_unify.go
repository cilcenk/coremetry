package chstore

import (
	"fmt"
	"strings"
)

// 0009 göçünün ADIM 3c'si — append-only tablolarda sınırlı yakalama.
//
// ADIM 3b (yakalama) ReplacingMergeTree'de bedava: aynı satırı tekrar
// yazmak zararsız, FINAL toplar. Beş MergeTree tablosunda yakalama YOK,
// çünkü tekrar insert çift satır üretir ve hiçbir şey onları toplamaz.
// Sonuç, göç dosyasının T2 tuzağının "kabul edilen" saniyelik boşluğu —
// ADIM 2 ile ADIM 3 arasında uygulamanın eski tabloya yazdığı satırlar
// `_old`'da kalır. audit_log'da bu bir bankada kabul edilebilir değil;
// incident_events incident zaman çizelgesi.
//
// Beşinin de zaman kolonu SIRALAMA ANAHTARINDA olduğu için pencere
// indeksli ve ucuz. Kesim noktası ADIM 2'den hemen sonra `_unified`
// üzerinden yakalanır; RENAME'den sonra yalnız o noktadan yenisi taşınır.
//
// NEDEN ANTI-JOIN. Bu kod tabanında bir tikin batch'i tek nanosaniyeyi
// paylaşabiliyor (v0.9.1306). Düz `>` o batch'in kesime düşen yarısını
// DÜŞÜRÜR, `>=` zaten kopyalanmışları ÇİFTLER. Pencereyi kesime
// sabitleyip anti-join yapmak ikisinden de kaçınır.
//
// ⚠ TEKİLLİK VARSAYILMAZ. Anahtar tuple'ı tekil değilse `NOT IN` aynı
// anahtarlı iki satırın İKİSİNİ birden düşürür — yakalama, önlemeye
// çalıştığı kaybı kendisi üretir. Ölçüldü (2026-08-23): incident_events'te
// 10.425 satırın 2'si BAYT BAYT aynı, yani hiçbir tuple onları ayıramaz.
// Bu yüzden karar bu tabloya GÖMÜLMEZ: çağıran önce
// stateCatchUpProbeSQL'i koşar, count() == uniqExact() değilse yakalamayı
// ATLAR ve gerekçeyi operatöre söyler.
//
// Sözleşmenin tek kaynağı migrations/0009_state_unify.sql içindeki
// `-- @catchup` satırlarıdır; buradaki tablo ona karşı test edilir
// (TestStateCatchUpSpecsMatchMigration).

// clusterReadSafe — `cluster()` kestirmesinin O TABLO İÇİN doğru olup
// olmadığı. ÖLÇÜLEN DEĞER, VARSAYIM DEĞİL.
//
// Sihirbaz ADIM 2'yi tek ifadeye indirmek için
//     INSERT INTO <t>_unified SELECT * FROM cluster(<küme>, db, <t>)
// yazıyor: `cluster()` shard başına BİR replika okur, yani bölünmüş bir
// tabloda tam olarak shard'ların birleşimini verir, çift sayım yok.
//
// ⚠ GEÇERLİLİK PENCERESİ DAR. Tablo ZATEN birleşikse (dört host tek
// replikasyon grubunda, hepsinde AYNI veri) `cluster()` yine shard başına
// bir replika okur — ama artık o replikalar aynı verinin kopyaları, yani
// sonuç N_shard KATINA çıkar. Ölçüldü (lokal, 2026-08-23): `problems`
// chc-0=4808, chc-1=4808, `cluster()` = 9616.
//
// Bu yüzden karar tabloya gömülemez, tablonun O ANKİ replikasyon şeklinden
// TÜRETİLİR: veri gerçekten shard'lara bölünmüşse her shard'ın kendi
// zookeeper_path'i vardır. distinctPaths == shardCount olduğunda ve
// SADECE o zaman `cluster()` ayrık dilimleri birleştirir.
//
//   distinctPaths == 1 && shardCount > 1  → tablo BİRLEŞİK; cluster()
//       veriyi shardCount katına çıkarır. INSERT ATMA.
//   distinctPaths < shardCount            → kısmen göç etmiş; bazı
//       shard'lar aynı grubu paylaşıyor, o pay çiftlenir. INSERT ATMA.
//   distinctPaths == shardCount           → her shard ayrı grup; güvenli.
func clusterReadSafe(distinctPaths, shardCount int) bool {
	if distinctPaths <= 0 || shardCount <= 0 {
		return false
	}
	return distinctPaths == shardCount
}

// stateCatchUpSpec, bir append-only tablonun sınırlı yakalama sözleşmesi.
type stateCatchUpSpec struct {
	// TimeCol pencereyi kuran zaman kolonu. Sıralama anahtarında
	// olmalı, yoksa pencere tam tarama olur.
	TimeCol string
	// Key tekillik tuple'ı. TimeCol'u İÇERİR: anti-join yalnız pencere
	// içinde karşılaştırma yapar, zaman kolonu tuple'ın dışında kalırsa
	// aynı anahtarlı farklı zamanlı satırlar birbirini eler.
	Key []string
}

var stateCatchUpSpecs = map[string]stateCatchUpSpec{
	"audit_log":        {TimeCol: "time", Key: []string{"time", "id"}},
	"notification_log": {TimeCol: "sent_at", Key: []string{"sent_at", "id"}},
	"ai_calls":         {TimeCol: "created_at", Key: []string{"created_at", "id"}},
	"incident_events":  {TimeCol: "time", Key: []string{"incident_id", "time", "kind", "ref_id"}},
	"monitor_results":  {TimeCol: "time", Key: []string{"monitor_id", "time"}},
}

// stateCatchUp, tablo için yakalama sözleşmesini döndürür. İkinci dönüş
// false ise o tabloda sınırlı yakalama TANIMLI DEĞİLDİR — çağıran
// yakalamayı atlar ve boşluğu operatöre bildirir.
func stateCatchUp(table string) (stateCatchUpSpec, bool) {
	sp, ok := stateCatchUpSpecs[table]
	return sp, ok
}

// backtickIdent — KİMLİK backtick'ler. quoteCHIdent adına rağmen kimlik
// değil STRING LİTERAL üretiyor (tek tırnak); onu buraya geçirmek
// `WHERE 'time' >= …` gibi sessizce yanlış ama geçerli SQL yazar.
// Adlar sabit bir tablodan geliyor, yine de hijyen: backtick içeren bir
// "ad" ayrıştırma hatasıdır, kaçırmak yerine temizlenir.
func backtickIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "") + "`"
}

// stateCatchUpKeyTuple, anahtar kolonlarını SQL tuple'ı olarak yazar.
func stateCatchUpKeyTuple(sp stateCatchUpSpec) string {
	parts := make([]string, len(sp.Key))
	for i, c := range sp.Key {
		parts[i] = backtickIdent(c)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// stateCatchUpCutSQL, kesim noktasını okur. ADIM 2'den HEMEN SONRA,
// RENAME'den ÖNCE koşulmalı: o anda `src` (`<t>_unified`) yalnız
// kopyalanan satırları içerir. RENAME sonrası okunursa uygulamanın yeni
// yazdıkları max'ı ileri iter ve pencere yanlış daralır.
func stateCatchUpCutSQL(sp stateCatchUpSpec, src string) string {
	return fmt.Sprintf("SELECT toString(max(%s)) FROM %s",
		backtickIdent(sp.TimeCol), backtickIdent(src))
}

// stateCatchUpProbeSQL, yakalamanın GÜVENLİ olup olmadığını ölçer:
// penceredeki satır sayısı ile tekil anahtar sayısı. Eşit değilse
// anti-join veri kaybeder — çağıran yakalamayı atlamalıdır.
func stateCatchUpProbeSQL(sp stateCatchUpSpec, src string) string {
	return stateCatchUpProbeFromSQL(sp, backtickIdent(src))
}

// stateCatchUpProbeFromSQL — kaynağı HAM ifade alan biçim. Sihirbaz
// `_old`'u `cluster(...)` üzerinden okur (tek node'dan iki shard'ın
// birleşimi); script tabloyu doğrudan okur.
func stateCatchUpProbeFromSQL(sp stateCatchUpSpec, srcExpr string) string {
	return fmt.Sprintf("SELECT count(), uniqExact(%s) FROM %s WHERE %s >= toDateTime64(?, 9)",
		stateCatchUpKeyTuple(sp), srcExpr, backtickIdent(sp.TimeCol))
}

// stateCatchUpInsertSQL, ADIM 3'ten sonra kalan satırları taşır.
// Pencere kesime sabitli, eleme anti-join ile — `>` / `>=` ikilemi yok.
func stateCatchUpInsertSQL(sp stateCatchUpSpec, dst, src string) string {
	return stateCatchUpInsertFromSQL(sp, dst, backtickIdent(src))
}

// stateCatchUpInsertFromSQL — kaynağı HAM ifade alan biçim. Eleme
// tarafı HER ZAMAN hedef tablodan okunur: hedef RENAME'den sonra zaten
// birleşiktir, `cluster()` ile okunsa iki kez sayılırdı.
func stateCatchUpInsertFromSQL(sp stateCatchUpSpec, dst, srcExpr string) string {
	tc := backtickIdent(sp.TimeCol)
	key := stateCatchUpKeyTuple(sp)
	var b strings.Builder
	fmt.Fprintf(&b, "INSERT INTO %s SELECT * FROM %s", backtickIdent(dst), srcExpr)
	fmt.Fprintf(&b, " WHERE %s >= toDateTime64(?, 9)", tc)
	fmt.Fprintf(&b, " AND %s NOT IN (", key)
	fmt.Fprintf(&b, "SELECT %s FROM %s WHERE %s >= toDateTime64(?, 9))",
		key, backtickIdent(dst), tc)
	return b.String()
}
