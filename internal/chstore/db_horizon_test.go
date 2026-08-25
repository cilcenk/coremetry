package chstore

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// v0.10.18 — F0.9a. /databases'in iki panelinin veri ufku farklı ve bu
// hiçbir yerde söylenmiyordu; boş receiver paneli üstelik YANLIŞ TEŞHİS
// basıyordu ("receiver kur" — oysa receiver çalışıyor, veri TTL'e düştü).
//
// Bu dosya iki şeyi koruyor: (1) elle tekrarlanan 90 sayısının CREATE
// ifadesindeki TTL'den ayrışmaması, (2) etkin ufkun BOOT değerinde
// donmaması.

// TestMVHorizonConstMatchesTheCreateTTL — sabit ile şemanın ayrışması.
//
// dbMVHorizonDays elle yazılmış bir 90; MV'nin TTL'i ise bir SQL dizgesi
// içinde. İkisini birbirine bağlayan hiçbir derleyici kontrolü YOK —
// biri TTL'i 45'e çekse sabit 90 kalır ve arayüz operatöre 90 gün
// vadetmeye devam eder. Yani düzeltmenin kendisi sessizce yalana döner.
func TestMVHorizonConstMatchesTheCreateTTL(t *testing.T) {
	b, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatalf("store.go okunamadı: %v", err)
	}
	src := string(b)

	// Her MV'nin CREATE'inden sonraki ilk TTL satırını al. Regex'i tek
	// parçada kurmak yerine iki adım: CREATE'i bul, sonra o noktadan
	// ileriye bakan pencerede TTL ara. (İlk yazımda tek regex kurmuştum
	// ve sayaç sözdizimi bozuktu — kapı gürültüyle düştü, sessizce
	// yeşile dönmedi; aşağıdaki `found != 2` koruması o yüzden var.)
	ttlRe := regexp.MustCompile(`TTL toDate\(time_bucket\) \+ INTERVAL (\d+) DAY`)
	found := 0
	for _, mv := range []string{"db_summary_5m", "db_caller_summary_5m"} {
		i := strings.Index(src, "CREATE MATERIALIZED VIEW IF NOT EXISTS "+mv)
		if i < 0 {
			t.Errorf("%s CREATE'i bulunamadı", mv)
			continue
		}
		win := src[i:]
		if len(win) > 800 {
			win = win[:800]
		}
		m := ttlRe.FindStringSubmatch(win)
		if m == nil {
			t.Errorf("%s CREATE'inde TTL satırı yok", mv)
			continue
		}
		found++
		if m[1] != "90" {
			t.Errorf("%s TTL'i %s gün ama dbMVHorizonDays = %d — arayüz yanlış ufuk ilan eder",
				mv, m[1], dbMVHorizonDays)
		}
	}
	if found != 2 {
		t.Fatalf("yalnız %d MV'nin TTL'i okunabildi; tarama bozulmuş olabilir ve "+
			"bu kapı sessizce yeşile dönmemeli", found)
	}
	if dbMVHorizonDays != 90 {
		t.Errorf("dbMVHorizonDays = %d; CREATE'ler 90 diyor", dbMVHorizonDays)
	}
}

// TestMVHorizonIsNotInTheRetentionPlan — "ayarlanamaz" iddiasının pini.
//
// db_horizon.go operatöre dolaylı olarak "üst panelin ufku ayardan
// DEĞİŞMEZ" diyor. Bu, SetRetention'ın planında hiçbir MV olmamasına
// dayanıyor. Biri yarın MV'yi plana eklerse iddia yalan olur ama hiçbir
// şey kırılmaz — bu test o boşluğu kapatıyor.
func TestMVHorizonIsNotInTheRetentionPlan(t *testing.T) {
	b, err := os.ReadFile("retention.go")
	if err != nil {
		t.Fatalf("retention.go okunamadı: %v", err)
	}
	plan := string(b)
	for _, mv := range []string{"db_summary_5m", "db_caller_summary_5m"} {
		if strings.Contains(plan, `"`+mv+`"`) {
			t.Errorf("%s artık SetRetention planında — ufku ayarlanabilir hale gelmiş, "+
				"db_horizon.go'daki 'ayarlanamaz' yorumu ve dbMVHorizonDays sabiti YANLIŞ", mv)
		}
	}
}

// TestHorizonReadsTheOverrideNotTheBootConfig — asıl tuzak.
//
// SetRetention `s.ret`i GÜNCELLEMİYOR (retention.go). Yani boot
// yapılandırmasını okumak, operatör saklamayı değiştirdikten sonra bayat
// bir sayı basmak demek — tam da önlemeye çalıştığımız yalanın aynısı,
// yalnız kaynağı biz oluruz. Kaynak taraması, çünkü canlı bir CH olmadan
// GetRetention çağrılamıyor; korunması gereken de DAVRANIŞ değil SIRA.
func TestHorizonReadsTheOverrideNotTheBootConfig(t *testing.T) {
	b, err := os.ReadFile("db_horizon.go")
	if err != nil {
		t.Fatalf("db_horizon.go okunamadı: %v", err)
	}
	src := stripGoCommentsCH(string(b))

	for _, fn := range []string{"receiverHorizonDays", "spanHorizonDays"} {
		i := strings.Index(src, "func (s *Store) "+fn)
		if i < 0 {
			t.Fatalf("%s bulunamadı", fn)
		}
		body := src[i:]
		if j := strings.Index(body[1:], "\nfunc "); j > 0 {
			body = body[:j]
		}
		gi := strings.Index(body, "s.GetRetention(ctx)")
		bi := strings.Index(body, "s.ret.")
		if gi < 0 {
			t.Errorf("%s canlı geçersiz kılmayı okumuyor — SetRetention s.ret'i güncellemediği için "+
				"operatör saklamayı değiştirdiğinde arayüz BAYAT sayı basar", fn)
			continue
		}
		if bi >= 0 && bi < gi {
			t.Errorf("%s boot yapılandırmasını geçersiz kılmadan ÖNCE okuyor — sıra ters", fn)
		}
	}
}

// TestZeroMeansSilence — yanlış sayı basmaktansa susmak.
//
// Ufuk çözülemezse 0 dönmeli; arayüz 0'ı "bilinmiyor" okuyup hiçbir şey
// ilan etmiyor. Buradaki tehlike, birinin 0 yerine "makul" bir varsayılan
// (7) koyması: o an arayüz ölçülmemiş bir sayıyı ölçülmüş gibi ilan eder.
func TestZeroMeansSilence(t *testing.T) {
	b, err := os.ReadFile("db_horizon.go")
	if err != nil {
		t.Fatalf("db_horizon.go okunamadı: %v", err)
	}
	src := stripGoCommentsCH(string(b))
	if !strings.Contains(src, "return 0") {
		t.Error("ufuk çözülemediğinde 0 dönen dal yok — bilinmeyen bir ufuk uydurma bir sayıyla ilan edilir")
	}
	for _, bad := range []string{"return 7", "return 30", "return 90\n}"} {
		if strings.Contains(src, bad) {
			t.Errorf("çözülemeyen ufuk için sabit varsayılan (%q) konmuş — ölçülmemiş sayı ilan edilir", bad)
		}
	}
}

// TestEnvelopeCarriesBothHorizons — kablolama pini.
//
// Saf hesap doğru ama zarfa yazılmıyorsa arayüz hiçbir şey göremez ve
// kusur yerinde kalır ("test edilmiş ama ulaşılamaz" sınıfı). İKİ kuruluş
// noktası var (MV dalı + env/raw dalı) ve ikisinde de `len(out) == 0`
// erken dönüşü var — beyan tam da o boş durumda gerekli, yani alanlar
// struct literal'ın İÇİNDE olmalı.
func TestEnvelopeCarriesBothHorizons(t *testing.T) {
	b, err := os.ReadFile("dependencies.go")
	if err != nil {
		t.Fatalf("dependencies.go okunamadı: %v", err)
	}
	src := stripGoCommentsCH(string(b))
	if n := strings.Count(src, "ReceiverHorizonDays: s.receiverHorizonDays(ctx)"); n != 2 {
		t.Errorf("receiver ufku %d kuruluş noktasında yazılıyor; İKİSİ de gerekli "+
			"(MV dalı + env/raw dalı)", n)
	}
	if !strings.Contains(src, "SpanHorizonDays:     s.spanHorizonDays(ctx, false)") {
		t.Error("MV dalı span ufkunu MV sabitiyle yazmıyor")
	}
	if !strings.Contains(src, "SpanHorizonDays:     s.spanHorizonDays(ctx, true)") {
		t.Error("env/raw dalı span ufkunu HAM saklamadan yazmıyor — o dalda ufuk 90 DEĞİL")
	}
}
