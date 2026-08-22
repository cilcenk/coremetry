// v0.9.1281 — otomatik verdict KAPISININ muhafızı.
//
// Bu test bir mutasyon denemesinin ürünü. Dilim gönderilmeden önce
// çalıştırılan mutasyonlardan biri — `if deep && s.autoVerdict != nil`
// içindeki `deep &&` koşulunu SİLMEK — hiçbir testi kızartmadı. Yani
// kapının kendisi ölçülmüyordu.
//
// Kaldırılmasının bedeli sessiz ve pahalı: sentezleyici 30 saniyede bir
// koşuyor ve batch'i açık CRITICAL problemlerin tamamı (prod'da 100+).
// Kapı gidince her tik, her problem için bir LLM çağrısı üretmeye
// çalışırdı — hata logu düşmez, yalnız kota biter. v0.9.200'ün devre
// kesicisini kuran olayın tam şekli.
//
// Neden kaynak taraması ve saf fonksiyon değil: kapı bir DALLANMA,
// hesap değil. Saf bir `shouldAutoVerdict(deep, hook)` yazmak
// totolojiyi test etmek olurdu — asıl risk fonksiyonun yanlış olması
// değil, ÇAĞRI YERİNİN onu atlamasıydı (mutasyonun ısırdığı yer
// çağrı yeridir).
package anomaly

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// workerLineComment — satır yorumlarını söker.
//
// ŞART: bu dosyanın ve worker'ın yorumları "autoVerdict" ve "deep"
// sözcüklerini geçiriyor. Yorumsuz okumasaydık test, düzeltmeyi
// ANLATAN yorumla eşleşip yanlış yeşil verirdi — bu kod tabanında iki
// kez yaşanmış bir tuzak (rca_record_test.go readAPISourceNoComments).
var workerLineComment = regexp.MustCompile(`(?m)^\s*//.*$`)

func TestAutoVerdictIsGatedOnDeepInvestigation(t *testing.T) {
	b, err := os.ReadFile("rootcause_worker.go")
	if err != nil {
		t.Fatalf("rootcause_worker.go okunamadı: %v", err)
	}
	src := workerLineComment.ReplaceAllString(string(b), "")

	lines := strings.Split(src, "\n")
	call := -1
	for i, ln := range lines {
		if strings.Contains(ln, "s.autoVerdict(") {
			call = i
			break
		}
	}
	if call < 0 {
		t.Fatal("s.autoVerdict(...) çağrısı bulunamadı — kanca hiç bağlanmıyor " +
			"ya da test bayatladı; ikisi de bakılmayı hak ediyor")
	}

	// Kapı çağrıyı DOMİNE etmeli, yani hemen üstünde olmalı. 3 satır:
	// gerçek muhafız bundan uzağa düşemez (düşerse zaten muhafız değil).
	//
	// Tarama call-1'den BAŞLAR: çağrının kendisi `if err := s.autoVerdict(…)`
	// biçiminde ve o satır da "if " ile başlıyor. call'dan başlasaydık
	// muhafız olarak ÇAĞRININ KENDİSİNİ bulur ve kapı silinse bile test
	// yeşil kalırdı — muhafızın kendini muhafız sanması.
	guard := ""
	for i := call - 1; i >= 0 && i >= call-3; i-- {
		if t := strings.TrimSpace(lines[i]); strings.HasPrefix(t, "if ") {
			guard = t
			break
		}
	}
	if guard == "" {
		t.Fatalf("s.autoVerdict çağrısının üstünde HİÇ if yok (satır %d) — otomatik "+
			"verdict her critical problemde koşar; sentezleyici 30sn'de bir tikliyor, "+
			"yani bu kotayı sessizce yakar", call+1)
	}
	if !strings.Contains(guard, "deep") {
		t.Fatalf("muhafız derin-soruşturma koşulunu taşımıyor: %q\n"+
			"Kapı KASTEN dar: yalnız P1 veya deploy-korelasyonlu vakalar "+
			"(shouldDeepInvestigate). Genişletmek yeni bir tetikleyici SINIFI "+
			"demek ve maliyeti açık critical problem sayısıyla çarpılır.", guard)
	}
	if !strings.Contains(guard, "&&") {
		t.Fatalf("muhafız birleşik değil: %q — derinlik koşulu ile kanca-nil "+
			"kontrolünün İKİSİ birden gerekli (biri VEYA'ya dönerse ya kapı "+
			"açılır ya nil kanca çağrılır)", guard)
	}
	if !strings.Contains(guard, "s.autoVerdict != nil") {
		t.Fatalf("muhafız nil-kanca kontrolünü kaybetmiş: %q — api/ingest "+
			"kiplerinde kanca bağlanmıyor, nil çağrı panikler", guard)
	}
}

// TestAutoVerdictRunsAfterHypothesisPersisted — SIRA sözleşmesi.
//
// Üretici h.Deep'i okuyor. Hipotez yazılmadan çağrılsaydı verdict,
// dayandığı kanıt kalıcı olmadan üretilirdi: operatör kararı görür ama
// "neye bakarak" sorusunun cevabı hiçbir yerde olmazdı — kalıcı gövdeyi
// eklemekle çözülen sorunun aynısı, bir katman ötede.
func TestAutoVerdictRunsAfterHypothesisPersisted(t *testing.T) {
	b, err := os.ReadFile("rootcause_worker.go")
	if err != nil {
		t.Fatalf("rootcause_worker.go okunamadı: %v", err)
	}
	src := workerLineComment.ReplaceAllString(string(b), "")

	// Problem kolundaki UpsertHypothesis — anomali kolundakinden SONRA
	// geleni. LastIndex ikisini de doğru ayırıyor çünkü otomatik verdict
	// yalnız problem kolunda var.
	upsert := strings.LastIndex(src, "s.store.UpsertHypothesis(ctx, h)")
	call := strings.Index(src, "s.autoVerdict(")
	if upsert < 0 || call < 0 {
		t.Fatal("UpsertHypothesis ya da autoVerdict çağrısı bulunamadı — test bayatladı")
	}
	if call < upsert {
		t.Error("otomatik verdict hipotez YAZILMADAN önce üretiliyor — üretici " +
			"h.Deep'i okuyor, yani verdict dayandığı kanıt kalıcı olmadan çıkar")
	}
}
