package chstore

import (
	"context"
	"fmt"
	"time"
)

// db_ownership.go — v0.9.1345, entity-model Faz 4b devamı.
//
// SORU: db-konulu bir problemin (v0.9.1338, `db:<system>@<instance>`)
// SAHİBİ kim?
//
// OPERATÖR KARARI (2026-08-24, itiraz üzerine TEKRARLANDI): "bir db
// öznesinin sahibi, onu EN ÇOK ÇAĞIRAN servisin sahip takımıdır."
// Kural budur; bu dosya onu uygular.
//
// ── Neden db_system KAPSAMINDA, instance kapsamında DEĞİL ────────────
//
// v0.9.1338'in ölçtüğü engel (identity.go'daki UYARI ve
// TestDBSubjectIDIsNotAHatAJoinKey) burada karşımıza çıkıyor: problemin
// taşıdığı `instance` HAT B'den (receiver metriği, `instance` attr'ı),
// çağıranları bilen db_caller_summary_5m'in `instance`'ı HAT A'dan
// (span, dbInstanceExpr → ilk basamak peer_service) geliyor. Kesişim
// SIFIR satır. Köprü uydurmak yasak.
//
// Lokal ölçüm (2026-08-24, 24s pencere, dağıtık chc-0/chc-1):
//
//	db_caller_summary_5m'de instance değerleri:
//	  oracle→'oracle', postgresql→'postgres', redis→'redis',
//	  clickhouse→'coremetry-monolithic', mongodb→'mongodb', …
//	Yani HAT A bu filoda db_system BAŞINA TEK bir instance taşıyor.
//	HAT B'nin gördüğü fiziksel adresler ('corebank-scan.prod',
//	'corebank-dg.prod') HAT A'da HİÇ yok.
//
// Bu yüzden çözüm db_system KAPSAMINDA yapılıyor: "Oracle'ı en çok kim
// çağırıyor", "corebank-scan.prod'u en çok kim çağırıyor" değil.
//
// ⚠️ BU BİR YAKLAŞIKLIKTIR ve ÜRÜN BUNU SÖYLEMEK ZORUNDA. Aynı db
// sisteminden İKİ küme (corebank-scan.prod, corebank-dg.prod)
// FARKLI takımlara aitse, ikisinin problemi de Oracle'ı toplamda en çok
// çağıran takıma yazılır. Bugünkü veride doğru cevabı veriyor (db_system başına
// tek instance), yarın bir küme daha eklendiğinde SESSİZCE yanılmaz —
// çünkü çözüm, türetildiği ÇAĞIRANIN adıyla birlikte taşınıyor
// (DBOwner.Caller → Problem.TeamsVia) ve yüzey onu "en çok çağıran X
// üzerinden türetildi" diye yazıyor. Bu repoda kendinden emin görünen
// yanlış cevap, çekinceli cevaptan KÖTÜdür.
//
// Gerçek instance-kapsamlı sahiplik ayrı bir dilimdir ve ÖNCE HAT A'ya
// fiziksel adresi taşıyan bir köprü ister (server.address host kısmı ya
// da operatör eşlemesi) — ÖLÇÜLEREK kurulur, uydurularak değil.

// DBCaller — bir çağıran servisin bir db_system'e karşı çağrı hacmi.
type DBCaller struct {
	Service string
	Calls   uint64
}

// DBOwner — çözülmüş sahiplik. Caller alanı KANITtır: takımlar hangi
// servisin katalog satırından geldi. Yüzeye o adla birlikte iniyor,
// çünkü yalnız takım adını göstermek yaklaşıklığı görünmez kılardı.
type DBOwner struct {
	Caller    string
	Calls     uint64
	OwnerTeam string
	SRETeam   string
}

// ResolveDBOwner — SAF. Çağıran satırları + katalog → sahiplik.
//
// ── Belirlenirlik (operatörün açık şartı) ───────────────────────────
//
// Baskın çağıran evaluator tikleri arasında SEKMEMELİ: sekerse problem
// yeniden atanır ve yeniden sayfa atar. İki mekanizma:
//
//  1. SIRALAMA TAM TANIMLI: önce Calls azalan, EŞİTLİKTE servis adı
//     artan. İkinci basamak olmadan cevap map/CH satır sırasına kalırdı
//     — ikisi de garantisiz. Lokal veride eşitliğe ÇOK yaklaşan çift
//     gerçekten var (billpay-service 11447 / sanctions-service 11446,
//     24s), yani bu teorik bir kaygı değil.
//  2. Pencere GENİŞ ve ızgaraya oturtulmuş (dbOwnershipWindow /
//     dbOwnershipWindowFor) — bir tazelemenin oynattığı pay pencerenin
//     %1'inden küçük.
//
// Kalan risk DÜRÜSTÇE söyleniyor: tepedeki iki çağıran birbirinin %1'i
// içindeyse sahiplik yine değişebilir. Bu "en çok çağıran" tanımının
// KENDİSİNDEN gelir; bir tie-break onu çözemez, yalnız TAM eşitliği
// çözer.
//
// ── ÇÖZÜLMEMİŞ dal = BUGÜNKÜ dal, bayt-bayt ─────────────────────────
//
// ok=false'ın üç yolu var ve üçünde de çağıran HİÇBİR ŞEY yazmaz, yani
// alanlar bugünkü gibi BOŞ kalır:
//
//	(a) çağıran yok       — MV'de bu db_system için satır yok.
//	(b) katalog satırı yok — baskın çağıran service_metadata'da yok.
//	(c) satır var, takım yok — owner_team ve sre_team İKİSİ de boş.
//
// (b) ve (c)'de İKİNCİ çağırana DÜŞÜLMEZ. Operatörün kuralı "en çok
// çağıranın takımı"; ikinciye düşmek ALAKASIZ bir takım yazmak olurdu
// ve yanlış takıma sayfa atmak, hiç sayfa atmamaktan kötüdür.
func ResolveDBOwner(callers []DBCaller, mds map[string]ServiceMetadata) (DBOwner, bool) {
	best, ok := dominantCaller(callers)
	if !ok {
		return DBOwner{}, false
	}
	md, found := mds[best.Service]
	if !found {
		return DBOwner{}, false
	}
	if md.OwnerTeam == "" && md.SRETeam == "" {
		// Satır var ama hiçbir şey söylemiyor. Kanıt olarak çağıranın
		// adını taşımanın da anlamı yok — "şu servisten türetildi" deyip
		// boş takım göstermek operatöre bir kusur gibi görünürdü.
		return DBOwner{}, false
	}
	return DBOwner{
		Caller:    best.Service,
		Calls:     best.Calls,
		OwnerTeam: md.OwnerTeam,
		SRETeam:   md.SRETeam,
	}, true
}

// dominantCaller — SAF: en çok çağıran satır, tam tanımlı sıralamayla.
//
// Adsız ve sıfır-çağrılı satırlar ELENİR: "en çok çağıran" olamazlar.
// Sıfır-çağrı MV'de countMerge'ün 0 döndüğü kenar durumdur; adsız satır
// bozuk bir service_name'dir. İkisi de kazanırsa cevap bir servise
// değil, bir boşluğa işaret ederdi.
func dominantCaller(callers []DBCaller) (DBCaller, bool) {
	var best DBCaller
	var found bool
	for _, c := range callers {
		if c.Service == "" || c.Calls == 0 {
			continue
		}
		if !found || c.Calls > best.Calls ||
			(c.Calls == best.Calls && c.Service < best.Service) {
			best, found = c, true
		}
	}
	return best, found
}

// ── Filo geneli anlık görüntü ───────────────────────────────────────

const (
	// dbOwnershipWindow — sahipliğin okunduğu geri-bakış.
	//
	// 24 SAAT, üç gerekçeyle (hepsi ölçüldü, 2026-08-24):
	//
	//  1. TAM BİR GÜNLÜK DÖNGÜ. Daha kısa bir pencere (1s/6s) yapısal
	//     olarak günün BİR EVRESİNE ait: gece 03:00'te koşan bir batch/
	//     raporlama işi o saatte veritabanının "en çok çağıranı" olur ve
	//     sahiplik gündüz OLTP servisine geri döner. Tam olarak brief'in
	//     uyardığı sekme. 24s, tek bir evrenin baskın olamayacağı EN KISA
	//     penceredir. (Lokal ölçüm: saat-saat 24 kovada da baskın çağıran
	//     account-service çıktı — yani bu filoda sekme YOK; pencere
	//     seçimi bugünkü veriye değil, YAPIYA karşı korunuyor.)
	//  2. YENİDEN ÖRGÜTLENME TAZELİĞİ. Bir servis kapatıldığında ya da
	//     trafiği devredildiğinde sahiplik BİR GÜN içinde takip eder.
	//     7 günlük bir pencere aynı işi bir HAFTA geciktirirdi; MV'nin
	//     90 günlük TTL'i çok daha uzununa da izin verirdi, bayatlık
	//     pahasına.
	//  3. MALİYET. 24s × 5dk kova = anahtar başına 288 kova. Dağıtık
	//     lokalde ölçüldü: 140ms, 116.774 satır, 2.45 MiB, 657 KiB bellek
	//     — filo GENELİ, TTL başına BİR kez.
	//
	// Not: 1s'ten 7g'ye kadar denenen HER pencere aynı kazananı verdi
	// (oracle→account-service). Yani bu seçim bugünkü cevabı değil,
	// yarınki KARARLILIĞI satın alıyor.
	dbOwnershipWindow = 24 * time.Hour

	// dbOwnershipTTL — anlık görüntünün süreç-içi tazeliği. MV 5 dakikalık
	// kova yazıyor, yani 5 dakikadan sık tazelemek YENİ BİLGİ üretemez;
	// 10 dakika async_insert gecikmesine bir tam kova pay bırakır.
	dbOwnershipTTL = 10 * time.Minute

	// dbOwnershipTopN — db_system başına saklanan çağıran sayısı.
	//
	// Sahiplik için 1 yeterdi; 1 SAKLANMIYOR çünkü o zaman baskınlık
	// kararını SQL verirdi ve saf yardımcının (ResolveDBOwner) test
	// edilecek bir işi kalmazdı — brief'in "mantık satır-taramasıyla
	// test edilen bir yerde yaşamasın" şartı tam olarak bu.
	// SQL calls-azalan sıraladığı için gerçek kazanan her zaman bu
	// N'in içindedir; kesme yalnız kuyruğu atar.
	dbOwnershipTopN = 10

	// dbOwnershipRowCap — toplam satır tavanı (devre kesici). db_system
	// kardinalitesi onlarla ölçülür; binlerce satır dönüyorsa varsayım
	// çökmüştür ve okuma sınırsız büyümek yerine kesilir.
	dbOwnershipRowCap = 2000
)

// dbOwnershipWindowFor — SAF: okumanın [start, end) sınırları.
//
// İKİ UÇ da 5 dakikalık MV ızgarasına oturtuluyor, aynı miktarda, yani
// kapsanan süre HER ZAMAN tam dbOwnershipWindow. Gerekçe belirlenirlik:
// aynı 5 dakikalık kova içinde yapılan iki çözüm BİREBİR aynı veriyi
// okur, dolayısıyla birbiriyle çelişemez. Yalnız alt ucu oturtan biçim
// (MVWindowStart deseni, v0.8.315) üst uçta YARIM bir kova bırakırdı;
// eşiğe bakan bir okumada bu doğru davranıştır (evaluator kapsanan
// saniyeye göre ölçekler), ama burada okunan şey bir ORANdır ve yarım
// kova her tazelemede oranı biraz oynatırdı.
func dbOwnershipWindowFor(now time.Time) (start, end time.Time) {
	end = now.Truncate(summaryMVBucket)
	return end.Add(-dbOwnershipWindow), end
}

// dbCallersSQL — hoisted so db_ownership_test.go can pin the bounds and
// the ordering the way serviceSeenSQL / deployMVCoverageProbeSQL do.
//
// CLAUDE.md sert kısıtı: zamanla sınırlı WHERE (indeksli time_bucket,
// MV'nin PARTITION BY'ı) + LIMIT + max_execution_time. Ham `spans` YOK —
// bu bir agregat okuması ve MV'si var (MV-first değişmezi).
//
// const DEĞİL, sabitlerden ÜRETİLİYOR: ilk yazımda limitler metnin içine
// elle yazılmıştı ve dbOwnershipTopN / dbOwnershipRowCap'in yanında
// İKİNCİ bir nüsha oluşturuyordu. Sabiti değiştirip metni unutmak
// derleyicinin göremeyeceği bir ayrışmadır — bu repoda kimlik
// zincirlerinin tekrar yazılmasıyla aynı sınıf (identity.go).
var dbCallersSQL = fmt.Sprintf(`
	SELECT db_system,
	       service_name,
	       countMerge(span_count_state) AS calls
	FROM db_caller_summary_5m
	WHERE time_bucket >= ? AND time_bucket < ?
	GROUP BY db_system, service_name
	ORDER BY db_system ASC, calls DESC, service_name ASC
	LIMIT %d BY db_system
	LIMIT %d
	SETTINGS max_execution_time = 10, `, dbOwnershipTopN, dbOwnershipRowCap)

// DBCallersBySystem — db_system → en çok çağıran ilk N servis.
//
// Filo GENELİ ve süreç-içi cache'li, problem BAŞINA sorgu DEĞİL. Gerekçe
// CLAUDE.md maliyet kuralı ve problem_counts_cache / service_seen
// (v0.9.1317) emsali: cevap sayfaya, filtreye, aralığa göre DEĞİŞMİYOR,
// dolayısıyla problem başına sormak aynı cevabı N kez hesaplamaktır.
//
// Dönen map PAYLAŞILIR (envMap/clusterMap deseni) — çağıranlar salt-okur.
func (s *Store) DBCallersBySystem(ctx context.Context) (map[string][]DBCaller, error) {
	s.dbOwnMu.RLock()
	if s.dbOwnVal != nil && time.Since(s.dbOwnAt) < dbOwnershipTTL {
		out := s.dbOwnVal
		s.dbOwnMu.RUnlock()
		return out, nil
	}
	s.dbOwnMu.RUnlock()

	start, end := dbOwnershipWindowFor(time.Now())
	rows, err := s.telemetryReadConn().Query(ctx, dbCallersSQL+s.shardSkipSetting(), start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]DBCaller{}
	for rows.Next() {
		var system, svc string
		var calls uint64
		if err := rows.Scan(&system, &svc, &calls); err != nil {
			return nil, err
		}
		out[system] = append(out[system], DBCaller{Service: svc, Calls: calls})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.dbOwnMu.Lock()
	s.dbOwnVal, s.dbOwnAt = out, time.Now()
	s.dbOwnMu.Unlock()
	return out, nil
}

// DBOwnerForSubject — bir db özne ID'si (`db:<system>@<instance>`) için
// sahiplik. ok=false her belirsiz halde (özne db değil, MV okunamadı,
// çağıran/katalog/takım yok) — çağıran o zaman HİÇBİR ŞEY yazmaz.
//
// instance BİLEREK kullanılmıyor: yukarıdaki kapsam kararı. Ayrıştırılıyor
// ki geçersiz bir ID sessizce db_system gibi okunmasın.
func (s *Store) DBOwnerForSubject(ctx context.Context, subjectID string) (DBOwner, bool) {
	system, _, ok := ParseDBSubjectID(subjectID)
	if !ok {
		return DBOwner{}, false
	}
	callers, err := s.DBCallersBySystem(ctx)
	if err != nil || len(callers[system]) == 0 {
		return DBOwner{}, false
	}
	mds, err := s.ListServiceMetadata(ctx)
	if err != nil || len(mds) == 0 {
		return DBOwner{}, false
	}
	return ResolveDBOwner(callers[system], mds)
}
