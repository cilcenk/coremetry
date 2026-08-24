package chstore

import (
	"os"
	"strings"
	"testing"
	"time"
)

// dbOwnParseTime — test-yerel RFC3339 ayrıştırıcı.
func dbOwnParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("geçersiz test zamanı %q: %v", s, err)
	}
	return ts
}

// db_ownership_test.go — v0.9.1345.
//
// Neyi pinliyor: db-konulu problemlerin sahiplik çözümü (operatör kuralı
// "en çok çağıranın takımı"). Üç şey testlenebilir bir yerde YAŞASIN diye
// saf fonksiyonlara çıkarıldı — brief'in şartı: mantık satır-içinde
// yaşarsa yalnız kaynak-taramasıyla test edilebilir.
//
//  1. BELİRLENİRLİK: baskın çağıran tikler arasında SEKMEZ. Sekerse
//     problem yeniden atanır ve yeniden sayfa atar.
//  2. ÇÖZÜLMEMİŞ DAL = BUGÜNKÜ DAL: çağıran/katalog/takım yoksa alanlar
//     BOŞ kalır. Asla tahmin, asla alakasız takıma düşme.
//  3. CH SINIRLARI: LIMIT + max_execution_time + zamanla sınırlı WHERE,
//     ve okuma MV'den (ham `spans` değil).

func TestResolveDBOwner(t *testing.T) {
	catalog := map[string]ServiceMetadata{
		"account-service": {Service: "account-service", OwnerTeam: "core-banking", SRETeam: "core-platform-sre"},
		"ledger-service":  {Service: "ledger-service", OwnerTeam: "payments", SRETeam: "core-platform-sre"},
		"sre-only":        {Service: "sre-only", SRETeam: "platform-ops"},
		"owner-only":      {Service: "owner-only", OwnerTeam: "data-platform"},
		"teamless":        {Service: "teamless"},
	}

	tests := []struct {
		name      string
		callers   []DBCaller
		wantOK    bool
		wantVia   string
		wantOwner string
		wantSRE   string
	}{
		{
			// Ölçülmüş gerçek şekil (2026-08-24, oracle 24s).
			name: "en çok çağıranın takımı kazanır",
			callers: []DBCaller{
				{"account-service", 49898},
				{"ledger-service", 35791},
			},
			wantOK: true, wantVia: "account-service",
			wantOwner: "core-banking", wantSRE: "core-platform-sre",
		},
		{
			// Girdi sırası CH satır sırasına bağlı; cevap DEĞİL.
			name: "girdi sırası cevabı DEĞİŞTİRMEZ",
			callers: []DBCaller{
				{"ledger-service", 35791},
				{"account-service", 49898},
			},
			wantOK: true, wantVia: "account-service",
			wantOwner: "core-banking", wantSRE: "core-platform-sre",
		},
		{
			// TAM eşitlik: ikinci basamak (servis adı artan) karar verir.
			// Bu basamak olmadan cevap map/CH sırasına kalırdı — ikisi de
			// garantisiz, yani sahiplik tikler arasında sekerdi.
			// Lokal veride eşitliğe 1 çağrı kalan çift GERÇEKTEN var
			// (billpay 11447 / sanctions 11446), yani teorik değil.
			name: "eşitlikte servis adı ARTAN kazanır",
			callers: []DBCaller{
				{"ledger-service", 5000},
				{"account-service", 5000},
			},
			wantOK: true, wantVia: "account-service",
			wantOwner: "core-banking", wantSRE: "core-platform-sre",
		},
		{
			name: "eşitlik tie-break'i ters girdi sırasında da AYNI",
			callers: []DBCaller{
				{"account-service", 5000},
				{"ledger-service", 5000},
			},
			wantOK: true, wantVia: "account-service",
			wantOwner: "core-banking", wantSRE: "core-platform-sre",
		},
		{
			// ÇÖZÜLMEMİŞ (a): hiç çağıran yok.
			name: "çağıran yok → çözülmedi", callers: nil, wantOK: false,
		},
		{
			name: "boş dilim → çözülmedi", callers: []DBCaller{}, wantOK: false,
		},
		{
			// ÇÖZÜLMEMİŞ (b): baskın çağıranın katalog satırı yok.
			// İKİNCİ çağırana DÜŞÜLMEZ — operatörün kuralı "en çok
			// çağıranın takımı"; ikinciye düşmek ALAKASIZ bir takıma
			// sayfa atmak olurdu.
			name: "baskın çağıranın katalog satırı yok → çözülmedi, İKİNCİYE düşülmez",
			callers: []DBCaller{
				{"katalogda-yok", 99999},
				{"account-service", 1},
			},
			wantOK: false,
		},
		{
			// ÇÖZÜLMEMİŞ (c): satır var, iki takım da boş.
			name:    "katalog satırı var ama takımlar boş → çözülmedi",
			callers: []DBCaller{{"teamless", 500}},
			wantOK:  false,
		},
		{
			// Yarım cevap GEÇERLİ: doğru satırdan gelen tek takım.
			// Servis problemlerinde de böyle davranıyor (EnrichProblems
			// WithTeams iki alanı da olduğu gibi kopyalar).
			name:    "yalnız SRE takımı olan satır çözülür",
			callers: []DBCaller{{"sre-only", 500}},
			wantOK:  true, wantVia: "sre-only", wantSRE: "platform-ops",
		},
		{
			name:    "yalnız owner takımı olan satır çözülür",
			callers: []DBCaller{{"owner-only", 500}},
			wantOK:  true, wantVia: "owner-only", wantOwner: "data-platform",
		},
		{
			// Sıfır çağrı "en çok çağıran" olamaz. countMerge'ün 0
			// döndüğü kenar durum kazanırsa cevap bir servise değil bir
			// boşluğa işaret ederdi.
			name: "sıfır-çağrılı satır kazanamaz",
			callers: []DBCaller{
				{"teamless", 0},
				{"account-service", 3},
			},
			wantOK: true, wantVia: "account-service",
			wantOwner: "core-banking", wantSRE: "core-platform-sre",
		},
		{
			name:    "yalnız sıfır-çağrılı satırlar → çözülmedi",
			callers: []DBCaller{{"account-service", 0}},
			wantOK:  false,
		},
		{
			// Adsız satır bozuk bir service_name'dir.
			name: "adsız satır elenir",
			callers: []DBCaller{
				{"", 99999},
				{"account-service", 7},
			},
			wantOK: true, wantVia: "account-service",
			wantOwner: "core-banking", wantSRE: "core-platform-sre",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ResolveDBOwner(tc.callers, catalog)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (got %+v)", ok, tc.wantOK, got)
			}
			if !tc.wantOK {
				// ÇÖZÜLMEMİŞ DAL BAYT-BAYT bugünkü: çağıran hiçbir alanı
				// yazmasın diye sıfır değer dönmek ZORUNDA.
				if got != (DBOwner{}) {
					t.Fatalf("çözülmemiş dal sıfır DBOwner dönmeli, %+v döndü — "+
						"dolu bir alan çağıranı yanlışlıkla yazmaya ikna edebilir", got)
				}
				return
			}
			if got.Caller != tc.wantVia {
				t.Errorf("Caller = %q, want %q", got.Caller, tc.wantVia)
			}
			if got.OwnerTeam != tc.wantOwner {
				t.Errorf("OwnerTeam = %q, want %q", got.OwnerTeam, tc.wantOwner)
			}
			if got.SRETeam != tc.wantSRE {
				t.Errorf("SRETeam = %q, want %q", got.SRETeam, tc.wantSRE)
			}
		})
	}
}

// TestResolveDBOwnerIsStableAcrossPermutations — belirlenirliğin kaba
// kuvvet hâli. Aynı küme, altı ayrı sırada: cevap DEĞİŞMEMELİ.
//
// Bir tik CH satırlarını bir sırada, sonraki tik başka sırada alabilir
// (dağıtık merge sırası garantisiz). Sıralamanın ikinci basamağı
// düşerse bu test kırılır; tek başına ilk basamak eşitlikte sırayı
// KORUR, yani hata sessizdir.
func TestResolveDBOwnerIsStableAcrossPermutations(t *testing.T) {
	catalog := map[string]ServiceMetadata{
		"aaa": {OwnerTeam: "team-a"},
		"bbb": {OwnerTeam: "team-b"},
		"ccc": {OwnerTeam: "team-c"},
	}
	base := []DBCaller{{"aaa", 100}, {"bbb", 100}, {"ccc", 100}}
	perms := [][]DBCaller{
		{base[0], base[1], base[2]},
		{base[0], base[2], base[1]},
		{base[1], base[0], base[2]},
		{base[1], base[2], base[0]},
		{base[2], base[0], base[1]},
		{base[2], base[1], base[0]},
	}
	for i, p := range perms {
		got, ok := ResolveDBOwner(p, catalog)
		if !ok {
			t.Fatalf("perm %d: çözülemedi", i)
		}
		if got.Caller != "aaa" || got.OwnerTeam != "team-a" {
			t.Fatalf("perm %d: %+v — üç yönlü TAM eşitlikte cevap sıraya "+
				"bağlı, yani sahiplik tikler arasında sekiyor", i, got)
		}
	}
}

// TestDBOwnershipWindowFor — pencere sınırları.
//
// İKİ UÇ da 5 dakikalık MV ızgarasına, AYNI miktarda oturmalı: aynı kova
// içindeki iki çözüm birebir aynı veriyi okusun (belirlenirlik) ve
// kapsanan süre her zaman TAM dbOwnershipWindow olsun.
//
// CLAUDE.md birim-karıştırma disiplini (v0.6.36): değer+birim taşıyan
// her şablon HER birimiyle test edilir. Burada birim tek (saat) ama
// tuzak aynı sınıftan — `toDate()`/`Truncate()` bir alt-gün hesabını
// sarınca sessizce gün başına yuvarlar. Bu yüzden hizalama vakaları
// (tam kova, kova ortası, kova sonu, gün sınırı) TEK TEK yazılı.
func TestDBOwnershipWindowFor(t *testing.T) {
	tests := []struct {
		name      string
		now       string
		wantStart string
		wantEnd   string
	}{
		{"tam kova sınırı — hiç kırpma yok",
			"2026-08-24T14:05:00Z", "2026-08-23T14:05:00Z", "2026-08-24T14:05:00Z"},
		{"kova ortası aşağı kırpılır",
			"2026-08-24T14:07:33Z", "2026-08-23T14:05:00Z", "2026-08-24T14:05:00Z"},
		{"kovanın son saniyesi hâlâ o kova",
			"2026-08-24T14:09:59Z", "2026-08-23T14:05:00Z", "2026-08-24T14:05:00Z"},
		{"nanosaniyeler de düşer",
			"2026-08-24T14:05:00.999999999Z", "2026-08-23T14:05:00Z", "2026-08-24T14:05:00Z"},
		{"gün sınırını geçen pencere — start bir önceki güne iner",
			"2026-08-24T00:02:00Z", "2026-08-23T00:00:00Z", "2026-08-24T00:00:00Z"},
		{"gece yarısı tam",
			"2026-08-24T00:00:00Z", "2026-08-23T00:00:00Z", "2026-08-24T00:00:00Z"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			now := dbOwnParseTime(t, tc.now)
			start, end := dbOwnershipWindowFor(now)
			if got := start.UTC().Format(time.RFC3339); got != tc.wantStart {
				t.Errorf("start = %s, want %s", got, tc.wantStart)
			}
			if got := end.UTC().Format(time.RFC3339); got != tc.wantEnd {
				t.Errorf("end = %s, want %s", got, tc.wantEnd)
			}
			// Kapsanan süre HER ZAMAN tam pencere. Yalnız bir ucu
			// oturtan bir yazım burada kırılır.
			if d := end.Sub(start); d != dbOwnershipWindow {
				t.Errorf("kapsanan süre = %v, want %v — iki uç AYNI miktarda "+
					"oturtulmazsa pencere her tazelemede biraz oynar", d, dbOwnershipWindow)
			}
			// Her iki uç da ızgarada.
			if !start.Equal(start.Truncate(summaryMVBucket)) {
				t.Errorf("start %v MV ızgarasında değil", start)
			}
			if !end.Equal(end.Truncate(summaryMVBucket)) {
				t.Errorf("end %v MV ızgarasında değil", end)
			}
		})
	}
}

// TestDBCallersSQLBounds — CLAUDE.md sert kısıtı, kaynak üzerinde.
func TestDBCallersSQLBounds(t *testing.T) {
	if !strings.Contains(dbCallersSQL, "max_execution_time") {
		t.Error("max_execution_time yok — sınırsız bir okuma CH'yi meşgul eder")
	}
	if !strings.Contains(dbCallersSQL, "time_bucket >= ? AND time_bucket < ?") {
		t.Error("zamanla sınırlı WHERE yok — MV'nin PARTITION BY'ı devre dışı kalır")
	}
	if !strings.Contains(dbCallersSQL, "LIMIT") {
		t.Error("LIMIT yok")
	}
	// MV-first değişmezi: bu bir agregat ve MV'si var.
	if !strings.Contains(dbCallersSQL, "FROM db_caller_summary_5m") {
		t.Error("okuma db_caller_summary_5m'den DEĞİL")
	}
	if strings.Contains(dbCallersSQL, "FROM spans") {
		t.Error("ham spans okuması — milyar satırda bu bir bug (MV-first)")
	}
	// Sıralamanın İKİ basamağı da SQL tarafında da duruyor: Go tarafı
	// kararı verir ama kesme (LIMIT n BY) SQL'de olduğu için gerçek
	// kazananın kesilmemesi calls-azalan sıraya bağlı.
	if !strings.Contains(dbCallersSQL, "ORDER BY db_system ASC, calls DESC, service_name ASC") {
		t.Error("LIMIT n BY kesmesi calls-azalan sıraya bağlı; sıra değişirse " +
			"gerçek kazanan sessizce kesilebilir")
	}
	// Limitlerin İKİNCİ bir nüshası olmamalı — metin sabitlerden üretiliyor.
	if !strings.Contains(dbCallersSQL, "LIMIT 10 BY db_system") {
		t.Errorf("dbOwnershipTopN (%d) SQL'e yansımamış", dbOwnershipTopN)
	}
	if !strings.Contains(dbCallersSQL, "LIMIT 2000") {
		t.Errorf("dbOwnershipRowCap (%d) SQL'e yansımamış", dbOwnershipRowCap)
	}
}

// TestDBOwnershipWindowIsAWholeNumberOfBuckets — pencere MV kovasının tam
// katı olmalı; olmasaydı iki uç aynı miktarda oturtulamaz ve kapsanan
// süre sabit kalmazdı.
func TestDBOwnershipWindowIsAWholeNumberOfBuckets(t *testing.T) {
	if dbOwnershipWindow%summaryMVBucket != 0 {
		t.Fatalf("pencere (%v) MV kovasının (%v) tam katı değil", dbOwnershipWindow, summaryMVBucket)
	}
	// TTL kovadan KISA olsaydı tazeleme yeni bilgi üretmeden CH'yi
	// meşgul ederdi.
	if dbOwnershipTTL < summaryMVBucket {
		t.Fatalf("TTL (%v) MV kovasından (%v) kısa — tazeleme yeni bilgi üretemez",
			dbOwnershipTTL, summaryMVBucket)
	}
}

// TestAnyDBSubject — filo-geneli okumayı YALNIZ gerçekten db konusu
// varsa tetikleyen kapı. Kapı ters çalışırsa (hep true) db problemi
// olmayan kurulumlar bedeli boşuna öder; hep false olursa özellik
// sessizce hiç çalışmaz.
func TestAnyDBSubject(t *testing.T) {
	tests := []struct {
		name string
		in   []Problem
		want bool
	}{
		{"boş dilim", nil, false},
		{"yalnız servis konuları", []Problem{
			{Service: "account-service", Kind: ProblemKindService},
			{Service: "ledger-service", Kind: ProblemKindService},
		}, false},
		{"boş kind = servis (eski satırlar)", []Problem{
			{Service: "account-service", Kind: ""},
		}, false},
		{"tek db konusu yeter", []Problem{
			{Service: "account-service", Kind: ProblemKindService},
			{Service: "db:oracle@corebank-scan.prod", Kind: ProblemKindDB},
		}, true},
		{"yalnız db konuları", []Problem{
			{Service: "db:oracle@corebank-scan.prod", Kind: ProblemKindDB},
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := anyDBSubject(tc.in); got != tc.want {
				t.Fatalf("anyDBSubject = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDBOwnershipScopeIsDocumented — KAPSAM KARARININ kapısı.
//
// Sahiplik db_system kapsamında çözülüyor, instance kapsamında DEĞİL,
// çünkü iki kimlik uzayı kesişmiyor (identity.go'daki ölçülmüş uyarı).
// Bu bir YAKLAŞIKLIK: aynı db sisteminden iki küme farklı takımlara
// aitse ikisi de aynı takıma yazılır.
//
// Test kodu değil, YAKLAŞIKLIK BEYANINI pinliyor — beyan kaybolduğu an
// bir sonraki okuyucu bunu kesin bir atıf sanar ve ürün de öyle
// göstermeye başlar. Bu repoda kendinden emin görünen yanlış cevap,
// çekinceli cevaptan kötüdür.
func TestDBOwnershipScopeIsDocumented(t *testing.T) {
	b, err := os.ReadFile("db_ownership.go")
	if err != nil {
		t.Fatalf("db_ownership.go okunamadı: %v", err)
	}
	src := string(b)
	for _, must := range []string{
		"YAKLAŞIKLIK",      // beyanın kendisi
		"SÖYLEMEK ZORUNDA", // ürünün yükümlülüğü
		"db_system",        // kapsam
		"FARKLI takım",     // patlama senaryosu
		"DBOwner.Caller",   // kanıtın nasıl taşındığı
	} {
		if !strings.Contains(src, must) {
			t.Errorf("kapsam/yaklaşıklık beyanından %q kaybolmuş — o beyan "+
				"olmadan türetilmiş sahiplik kesin bir atıf gibi okunur", must)
		}
	}
}
