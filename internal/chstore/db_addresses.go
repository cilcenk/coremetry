package chstore

import "context"

// db_addresses.go — /database kimliğinin KAÇ FİZİKSEL ADRESİ kapsadığı
// (v0.10.19, F0.8'in ölçümlü yarısı).
//
// ── KUSUR ───────────────────────────────────────────────────────────────
//
// Sayfanın kimliği (db_system, instance, db_name) üçlüsü ve `instance`
// aslında `peer_service`. MV bunu ORDER BY anahtarı olarak kullanıyor
// (store.go: db_summary_5m / db_caller_summary_5m), yani AYNI
// peer.service'i paylaşan farklı fiziksel adresler TEK satıra çöküyor.
//
// Bu çökme kasıtlı ve doğru: topoloji grafiğinde çekirdek bankacılık
// veritabanı tek bir düğüm olmalı. Kusur çökmenin kendisi değil,
// SÖYLENMEMESİ: operatör bir Oracle RAC SCAN adresi ile Data Guard
// yedeğinin TOPLAMINA bakarken tek bir makineye baktığını sanıyor.
//
// ── NEDEN AYRI BİR TARAMA GEREKİYOR ─────────────────────────────────────
//
// `server.address` MV'lerde YOK; yalnız ham span'lerin attr dizisinde.
// MV'ye eklemek REDDEDİLDİ (G6 operatör kararı: "bugünkü kapsamla yaşa"),
// o yüzden ölçüm ham `spans`ten geliyor.
//
// ── MALİYET: NEDEN BU KABUL EDİLEBİLİR ──────────────────────────────────
//
// `spans` birincil anahtarı (service_name, time). Servis yüklemi OLMADAN
// bu tarama budanamaz ve milyar-span ölçeğinde zaman aşımına uğrar —
// v0.7.35'te operatör tam bunu bildirmişti ("top statements blank at
// 1000s of services / 100+ DBs").
//
// Bu yüzden prob YENİ bir yol açmıyor: GetDatabaseDetail'in ZATEN
// koşulduğu ifade taramasının AYNI budanmış yüklemini paylaşıyor
// (service_name IN (çağıranlar) + zaman sınırı + system/instance/db_name).
// Marjinal maliyet, halihazırda ödenen taramanın bir eşi kadar.
//
// ⚠ BAĞIMLILIK: bu gerekçe `topOps` ham taramasının VARLIĞINA dayanıyor.
// F0.1 o taramayı silmeyi ya da db_statement_summary_5m'e taşımayı
// tartışıyor; öyle olursa bu prob tek başına kalan tek ham tarama olur ve
// maliyet gerekçesi YENİDEN değerlendirilmelidir.
//
// ── SESSİZLİK SÖZLEŞMESİ ────────────────────────────────────────────────
//
// Prob düşerse ya da tavana çarparsa `probed=false` dönüyor ve arayüz
// HİÇBİR ŞEY ilan etmiyor. Bunun alternatifi — boş sonucu "1 adres" diye
// okumak — tekilliği YANLIŞ yere iddia etmek olurdu ve bu, hiçbir şey
// dememekten kötüdür.

// dbAddrProbeCap — toplanacak azami adres. Görüntülemek için 2-3 yeter;
// 6. eleman "6+" demeyi mümkün kılıyor. Sınır aynı zamanda agreganın
// belleğini sabitliyor: patolojik bir kurulumda (her pod ayrı adres)
// sınırsız groupUniqArray bellek yerdi.
const dbAddrProbeCap = 6

// dbAddrSelectSQL — sınırlı, ucuz agrega. uniqExact yerine
// groupUniqArray(N): hem sayıyı hem ADRESLERİN KENDİSİNİ veriyor
// (operatöre "2 adres" demek yerine hangileri olduğunu göstermek için)
// ve durumu N ile sabit.
const dbAddrSelectSQL = `groupUniqArray(` + "6" + `)(attr_values[indexOf(attr_keys, 'server.address')])`

// DBPhysicalAddrs — bir kimliğin kapsadığı fiziksel adresler.
type DBPhysicalAddrs struct {
	// Probed — ölçüm GERÇEKTEN yapıldı mı. false iken Addrs anlamsızdır
	// ve arayüz hiçbir şey ilan etmemelidir. Boş sonucu "tek adres" diye
	// okumak, tekilliği yanlış yere iddia etmek olurdu.
	Probed bool     `json:"probed"`
	Addrs  []string `json:"addrs,omitempty"`
	// Capped — tavana dayandı, gerçek sayı daha yüksek olabilir.
	Capped bool `json:"capped,omitempty"`
}

// Count — kaç adres. Tavana dayandıysa "en az" anlamındadır.
func (d DBPhysicalAddrs) Count() int { return len(d.Addrs) }

// probeDBAddresses — ham span'lerden adres kümesini toplar.
//
// whereSQL/args ÇAĞIRANDAN geliyor ve ifade taramasınınkiyle BİREBİR
// aynı olmak zorunda: iki farklı yüklem, aynı çekmecede iki farklı
// kapsam demek olurdu (v0.9.821'in aynı dosyada düzelttiği kusur).
func (s *Store) probeDBAddresses(
	ctx context.Context, whereSQL string, args []any,
) DBPhysicalAddrs {
	// max_execution_time KISA (5s): bu bir SÜS bilgisi, sayfanın
	// omurgası değil. Tavana çarparsa sessizce vazgeçiyoruz — operatörü
	// bir beyan uğruna bekletmek yanlış takas.
	q := `SELECT ` + dbAddrSelectSQL + ` FROM spans WHERE ` + whereSQL +
		` SETTINGS max_execution_time = 5`
	rows, err := s.telemetryReadConn().Query(ctx, q, args...)
	if err != nil {
		return DBPhysicalAddrs{}
	}
	defer rows.Close()
	var addrs []string
	if rows.Next() {
		if err := rows.Scan(&addrs); err != nil {
			return DBPhysicalAddrs{}
		}
	}
	if err := rows.Err(); err != nil {
		return DBPhysicalAddrs{}
	}
	out := DBPhysicalAddrs{Probed: true, Capped: len(addrs) >= dbAddrProbeCap}
	// Boş dizge, `server.address` taşımayan span'lerden gelir (ör. eski
	// SDK, net.peer.name kullanan sürüm). Adres DEĞİL, o yüzden düşüyor —
	// aksi hâlde "2 adres" derken biri boş olurdu.
	for _, a := range addrs {
		if a != "" {
			out.Addrs = append(out.Addrs, a)
		}
	}
	return out
}
