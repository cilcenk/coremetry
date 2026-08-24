package api

import (
	"strings"
	"testing"
)

// problems_db_ownership_test.go — v0.9.1345.
//
// db-konulu problemlerin (v0.9.1338) sahipliği artık TÜRETİLİYOR: "bu
// veritabanını en çok çağıran servisin takımı" (operatör kararı
// 2026-08-24, chstore/db_ownership.go).
//
// ── BU DOSYANIN KORUDUĞU ÇELİŞKİ ────────────────────────────────────
//
// Takım süzgeci SQL'e bir SERVİS ADI allowlist'i olarak iniyor
// (servicesForTeam, v0.9.342 perf düzeltmesi). Bir db öznesinin `service`
// alanı bir DBSubjectID (`db:oracle@corebank-scan.prod`) ve o listede
// ASLA olamaz. Yani sahiplik verilip kaçış kapısı VERİLMEZSE ürün kendi
// kendisiyle çelişir: satırın çipi "core-banking" yazarken
// owner=core-banking süzgeci o satırı SESSİZCE gizler.
//
// Kaçış kapısı TEK BAŞINA da yeterli değil ve ters yönde tehlikeli:
// gevşetilmiş SQL BÜTÜN db problemlerini içeri alır, dolayısıyla Go
// tarafında KESİN eşleştirme (matchesTeamFilter, zenginleştirilmiş
// OwnerTeam üzerinden) olmak ZORUNDA. İkisi BİRLİKTE doğru; biri
// eksikken ürün ya satır gizler ya başka takımın satırını gösterir.
//
// Neden kaynak taraması: bu iki handler canlı bir Store olmadan
// çalıştırılamıyor. inbox_team_scope_test.go (v0.9.353) aynı sınırla
// aynı çözümü kullanıyor — bu dosya onun /problems + /problems/buckets
// tarafındaki ikizi. Saf karar zaten ayrı ve tablo-testli
// (chstore.problemServicesConjunct).

// ⚠️ KAYNAK TARAYAN GATE YORUMLARI OKUMAMALI (v0.9.1345, ilk yazımın
// bulgusu). Bu dosyanın sıralama kontrolü ÖNCE yanlış alarm verdi: aranan
// `matchesTeamFilter` adı, o çağrıyı AÇIKLAYAN yorumda çağrıdan ÖNCE
// geçiyordu. Yani gate kodu değil PROZAYI ölçtü.
//
// Aynı kusur ters yönde çok daha tehlikeli: bir yorumda geçen ad, silinmiş
// bir çağrının yerine geçip gate'i SUSTURABİLİR. Bu yüzden tarama
// yorumlardan arındırılmış kaynağa bakıyor.
//
// stripLineComments yalnız `//` satır yorumlarını atar — bu dosyanın
// baktığı bölgede blok yorum yok ve dizgi literali içinde `//` geçmiyor
// (kontrol edildi). Daha fazlası bir Go ayrıştırıcısı ister ve bu gate'in
// kazandığından fazlasını maliyet olarak getirirdi.
func stripLineComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// TestProblemsTeamFilterLetsDBSubjectsThrough — /api/problems listesi.
func TestProblemsTeamFilterLetsDBSubjectsThrough(t *testing.T) {
	src := stripLineComments(readSrc(t, "api.go"))

	i := strings.Index(src, "scan.Services = servicesForTeam(")
	if i < 0 {
		t.Fatal("listProblems takım allowlist'ini çözmüyor")
	}
	// Kaçış kapısı allowlist ÇÖZÜMÜYLE aynı dalda olmalı: allowlist
	// kurulmadığında (katalog blip'i) daraltma zaten kapalıdır ve
	// gevşetmenin anlamı yoktur.
	window := srcWindow(src, i, 900)
	if !strings.Contains(window, "scan.ServicesAllowDBSubjects = true") {
		t.Error("listProblems: db özneleri için kaçış kapısı YOK — sahiplikleri " +
			"türetildiği hâlde owner/sre süzgeci onları SQL'de eler; satırın " +
			"çipi bir takım yazarken o takımın süzgeci satırı gizler")
	}

	// ...ve gevşetmenin karşılığı olan KESİN eşleştirme hâlâ orada.
	if !strings.Contains(src, "matchesTeamFilter(ta, p.OwnerTeam, p.SRETeam, ownerTeam, sreTeam)") {
		t.Error("listProblems: kesin takım eşleştirmesi kaybolmuş — gevşetilmiş " +
			"SQL bütün db problemlerini içeri alır, Go kapısı olmadan operatör " +
			"BAŞKA takımların veritabanı alarmlarını görür")
	}
}

// TestProblemBucketsAgreeWithTheList — chip sayıları ile satırlar aynı
// evreni saymalı.
//
// v0.9.474 bu ıraksamayı bir kez düzeltti (takım/küme süzgeci listeye
// iniyordu, chip'lere inmiyordu — "takım seçili operatörde P1 chip'i tüm
// filoyu sayıyordu"). db sahipliği aynı ıraksamayı YENİDEN açabilirdi:
// liste db satırını gösterip chip onu saymazsa (ya da tersi) operatör iki
// sayı görür ve hangisinin doğru olduğunu bilemez.
func TestProblemBucketsAgreeWithTheList(t *testing.T) {
	src := stripLineComments(readSrc(t, "api.go"))

	i := strings.Index(src, "f.Services = servicesForTeam(")
	if i < 0 {
		t.Fatal("listProblemBuckets takım allowlist'ini çözmüyor")
	}
	window := srcWindow(src, i, 900)
	if !strings.Contains(window, "f.ServicesAllowDBSubjects = true") {
		t.Error("buckets: kaçış kapısı YOK — chip'ler db problemlerini saymaz " +
			"ama liste gösterir (v0.9.474'ün kapattığı ıraksamanın db tarafı)")
	}

	// Gevşetmenin karşılığı: buckets Go'da SAYIYOR, yani kesin
	// eşleştirmeyi de kendisi yapmak zorunda. Listenin ikinci geçişi
	// buraya OTOMATİK gelmiyor — iki ayrı handler.
	// ⚠️ ARAMA PENCERESİ buckets HANDLER'INA HAPSEDİLİYOR.
	//
	// İkinci mutasyon bulgusu: pencere dosya sonuna kadar açıkken, guard'ı
	// etkisizleştiren mutasyon YİNE ısırmadı — çünkü dizinin aradığı
	// `ownerTeam != "" || sreTeam != ""` metni bir SONRAKİ fonksiyonda
	// (listProblems) da geçiyor ve tarama oraya taşıp "buldum" dedi.
	// Kaynak tarayan bir gate için sınır, aranan metin kadar önemli:
	// sınırsız bir pencere komşu fonksiyonun kodunu bu fonksiyonun
	// kanıtı sayar.
	if end := strings.Index(src[i:], "\nfunc (s *Server) "); end > 0 {
		src = src[:i+end]
	}

	// SIRALI DİZİ olarak kontrol ediliyor, tek tek adların varlığı olarak
	// değil.
	//
	// GEREKÇE (mutasyon bulgusu): "kesin eşleştirmeyi ETKİSİZLEŞTİR"
	// mutasyonu — guard'ı `if false` yapmak — iki çağrıyı da METİNDE
	// bıraktığı için "adlar var mı" biçimindeki bir gate'i ISIRMIYORDU.
	// Dal ölü değil, gate kördü (v0.9.1280 sınıfı). Guard'ın KENDİSİNİ
	// diziye almak o mutasyonu görünür kılıyor.
	//
	// Sıra da sözleşmenin parçası: zenginleştirme eşleştirmeden ÖNCE
	// koşmalı, yoksa matchesTeamFilter boş OwnerTeam/SRETeam görür ve db
	// satırlarının HEPSİNİ eler (sessiz sıfır).
	seq := []struct{ frag, why string }{
		{"ListProblems(ctx, f)", "buckets satırları hiç okumuyor"},
		{`ownerTeam != "" || sreTeam != ""`,
			"gevşetilmiş SQL'in karşılığı olan takım guard'ı YOK/etkisiz — " +
				"chip'ler BAŞKA takımların veritabanı alarmlarını sayar"},
		{"EnrichProblemsWithTeams", "takımlar çözülmeden eşleştirme yapılıyor"},
		{"matchesTeamFilter", "kesin takım eşleştirmesi YOK"},
	}
	at := i
	for _, step := range seq {
		j := strings.Index(src[at:], step.frag)
		if j < 0 {
			t.Fatalf("buckets: %q sırada bulunamadı — %s", step.frag, step.why)
		}
		at += j + len(step.frag)
	}
}

// srcWindow — kaynağın [from, from+n) dilimi, sınır güvenli.
func srcWindow(src string, from, n int) string {
	to := from + n
	if to > len(src) {
		to = len(src)
	}
	return src[from:to]
}
