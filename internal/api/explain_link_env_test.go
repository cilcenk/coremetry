package api

import (
	"strings"
	"testing"
)

// explain_link_env_test.go — v0.10.55.
//
// Operatör bildirdi: "Explain trace dediğimde requestid geliyor ama
// logizlemeye linklemesi için chatten devam etmem gerekiyor."
//
// Asıl sebep DEPLOY farkıydı (satır içi köprü v0.10.35, prod v0.10.26).
// Ama o araştırma sırasında GERÇEK bir kusur çıktı: deliverExplain
// kimlik köprüsünün servisini YALNIZ `?service=` query param'ından
// okuyordu ve ✨ Explain uçlarının HİÇBİRİ onu göndermiyor.
//
// ⚠ Sonuç sessiz ve prod'da GÖRÜNMEZ: envFromServiceName("") boş döner,
// kod "prod"a düşer ve prod'da doğru cevabı verir. Prod-DIŞI bir trace
// açıklandığında ise link YANLIŞ ortamın log sistemine gider — operatör
// tıklar, aradığı kaydı bulamaz ve kaydın var olmadığını sanır.
//
// "Bugün doğru" ≠ doğru: doğruluk, ortamın prod olmasına asılıydı
// ([[feedback-correctness-held-by-a-setting]]).

func TestExplainPassesServiceForLinkEnv(t *testing.T) {
	src := readSourceFile(t, "copilot_explain_stream.go")

	// Servis artık AÇIK bir parametre; query param yalnız YEDEK.
	if !strings.Contains(src, "run explainRun, service string)") {
		t.Error("deliverExplain servisi açık parametre olarak almıyor — " +
			"env yine query param'a (ve oradan 'prod' varsayılanına) düşer")
	}
	iSvc := strings.Index(src, "svc := service")
	iFallback := strings.Index(src, `svc = r.URL.Query().Get("service")`)
	if iSvc < 0 || iFallback < 0 {
		t.Fatal("servis çözümü bulunamadı — test bayatlamış")
	}
	if iSvc > iFallback {
		t.Error("query param açık parametreyi EZİYOR — handler'ın bildiği " +
			"servis yok sayılır")
	}
}

// TestTraceExplainUsesRootService — trace'in ortamı KÖK servisten.
//
// StackService DEĞİL: o, stacktrace'i BASAN servis ve trace'te stacktrace
// olmayabilir (o zaman boş kalır ve env yine prod'a düşerdi). Kök servis
// her trace'te var.
func TestTraceExplainUsesRootService(t *testing.T) {
	// ⚠ Boşluğa DUYARSIZ eşleşme. gofmt alan adlarını hizalıyor
	// ("RootService  string", iki boşluk) ve tek-boşluk arayan bir kapı,
	// alan yerindeyken kırmızıya döner. Bu gece aynı sınıf iki kez
	// ısırdı (satır sarması, eklemeli sözcük): kaynak tarayan bir kapı
	// boşluğu ÖNCE normalize etmeli.
	in := flatWS(readSourceFile(t, "explain_trace_input.go"))
	if !strings.Contains(in, "RootService string") {
		t.Error("traceExplainInput kök servisi taşımıyor")
	}
	if !strings.Contains(in, "RootService: rootService") {
		t.Error("kök servis doldurulmuyor — alan var ama boş gider")
	}
	// Kök seçimi parent'sız span'e dayanmalı; kırık zincirde en erken.
	if !strings.Contains(in, `if sp.ParentSpanID == ""`) {
		t.Error("kök span parent'sızlıkla seçilmiyor")
	}

	api := readSourceFile(t, "api.go")
	if !strings.Contains(api, "}, run, in.RootService)") {
		t.Error("trace explain kök servisi deliverExplain'e GEÇİRMİYOR — " +
			"prod-dışı trace'in linki yanlış ortama gider")
	}
}

// flatWS — ardışık boşlukları teke indirir. gofmt hizalaması yüzünden
// kaynak taramasının yanlış negatif vermesini engeller.
func flatWS(s string) string { return strings.Join(strings.Fields(s), " ") }
