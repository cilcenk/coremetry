package copilot

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ─── Prompt sicili ──────────────────────────────────────────────────
//
// v0.9.1128 (Faz 1.6) — bu dosya v0.8.374'ten beri yalnız copilot.go'da
// tanımlı prompt'ları çiviliyordu ve sicil ELLE tutuluyordu: haritaya
// eklenmeyen bir prompt sessizce denetimsiz kalıyordu. internal/api'de
// yaşayan beş prompt (serviceAnalysis, guidedChat, drawerChat, ragChat,
// chat) tam olarak böyle GÖRÜNMEZDİ — hiçbiri bu testin haritasında
// değildi, çünkü başka bir pakette duruyorlardı.
//
// Yeni mekanizma: sicil accessor'lardan TÜRETİLİR. prompts.go'daki her
// `func SystemPromptX() string` tam olarak BİR kovaya yazılmak
// zorunda (TestPromptRegistryCoversEveryAccessor). Prompt eklemek =
// sicile satır eklemek; unutmak = kırmızı test.

type promptClass int

const (
	// classDirective — düz metin (prose) yüzeyi; ortak dil direktifiyle
	// BİTMELİ. v0.8.374 operatör kararı: "hepsi Türkçe".
	classDirective promptClass = iota
	// classTurkishNative — talimatın KENDİSİ Türkçe yazılmış prompt.
	// Ortak direktif gereksiz (metin zaten Türkçe konuşuyor), ama
	// prompt'un İngilizceye kayması da sessiz bir davranış değişimi
	// olurdu; o yüzden Türkçe çıpası aranır.
	classTurkishNative
	// classStructured — makine-parse edilen çıktı (DSL JSON, öneri
	// listesi). Dil direktifi TAŞIMAZ: direktif, parse edilen çıktının
	// etrafına düzyazı davet eder.
	classStructured
)

// promptRegistry — accessor adı (SystemPrompt öneki atılmış hâli) →
// sınıf. Bu harita YÜZEY sicilidir; prompts.go'daki accessor kümesiyle
// birebir örtüşmek zorunda.
func promptRegistry() map[string]promptClass {
	return map[string]promptClass{
		// ── Bir-atımlık ✨ Explain yüzeyleri + kod ikizleri
		"Trace":             classDirective,
		"TraceWithCode":     classDirective,
		"Span":              classDirective,
		"Problem":           classDirective,
		"Exception":         classDirective,
		"ExceptionWithCode": classDirective,
		"Incident":          classDirective,
		"Anomaly":           classDirective,
		"ServiceHealth":     classDirective,
		"ServiceCharts":     classDirective,
		"Runbook":           classDirective,
		"CompareTraces":     classDirective,
		"DeployImpact":      classDirective,
		"SLOBurn":           classDirective,
		"SlowQuery":         classDirective,
		// ── Chat kademeleri (Faz 1.6'da internal/api'den taşındı)
		"GuidedChat": classDirective, // kademe 1 — guided router narration
		// v0.10.13 — talimatın kendisi Türkçe; cevabı BAĞLAMDAN harfi
		// harfine kopyalatıyor (küçük model kendi adı yerine tanınmış bir
		// marka söylemeye meyilli).
		"SelfMeta":   classTurkishNative,
		"DrawerChat": classDirective, // kademe 2 — explain-grounded çekmece
		// ── Türkçe-native talimatlar. İkisi (RCAVerdict, ServiceAnalysis)
		// JSON üretir ama talimatı Türkçedir: 2B dersi (copilot_aianalyze.go)
		// — küçük modelde "İngilizce talimat + Türkçe cevap" kod-değiştirme
		// vergisidir. Bu yüzden classStructured'ın "Türkçe geçmesin" kuralı
		// onlara UYGULANMAZ; ayrım çıktının şekli değil, TALİMATIN dilidir.
		"RCAVerdict":      classTurkishNative,
		"ServiceAnalysis": classTurkishNative, // JSON çıktı, Türkçe talimat
		"RAGChat":         classTurkishNative, // kademe 3 — doküman yolu
		// v0.9.1232 — kademe 4 (serbest tool döngüsü) classDirective'ten
		// buraya taşındı: metin artık Türkçe yazılmış. ChatRoundCap aynı
		// döngünün tur-tavanı hâli; sicile girmesiyle copilot_chat.go'daki
		// satır-içi İngilizce ek de dil kapısının kapsamına girdi.
		"Chat":          classTurkishNative,
		"ChatRoundCap":  classTurkishNative,
		"ShiftSummary":  classTurkishNative,
		"AlertNoise":    classTurkishNative,
		"LogPatterns":   classTurkishNative,
		"Postmortem":    classTurkishNative, // Faz 5.4 — markdown taslak, Türkçe talimat
		"RunbookUpdate": classTurkishNative, // Faz 5.5 — güncelleme önerisi bloğu
		// ── Makine-parse edilen çıktı
		"NLToQuery":       classStructured,
		"CHQueryOptimize": classStructured,
		"ServiceTags":     classStructured,
	}
}

// promptTexts — sicildeki her ad için ÇALIŞMA ZAMANI metni. Accessor'ı
// çağırır; böylece test const'u değil, handler'ın gerçekten gönderdiği
// dizeyi denetler.
func promptTexts() map[string]string {
	return map[string]string{
		"Trace": SystemPromptTrace(), "TraceWithCode": SystemPromptTraceWithCode(),
		"Span": SystemPromptSpan(), "Problem": SystemPromptProblem(),
		"Exception": SystemPromptException(), "ExceptionWithCode": SystemPromptExceptionWithCode(),
		"Incident": SystemPromptIncident(), "Anomaly": SystemPromptAnomaly(),
		"ServiceHealth": SystemPromptServiceHealth(), "ServiceCharts": SystemPromptServiceCharts(),
		"Runbook": SystemPromptRunbook(), "CompareTraces": SystemPromptCompareTraces(),
		"DeployImpact": SystemPromptDeployImpact(), "SLOBurn": SystemPromptSLOBurn(),
		"SlowQuery": SystemPromptSlowQuery(), "GuidedChat": SystemPromptGuidedChat(),
		"SelfMeta":   SystemPromptSelfMeta(),
		"DrawerChat": SystemPromptDrawerChat(), "Chat": SystemPromptChat(),
		"ChatRoundCap": SystemPromptChatRoundCap(),
		"RCAVerdict":   SystemPromptRCAVerdict(), "ServiceAnalysis": SystemPromptServiceAnalysis(),
		"RAGChat": SystemPromptRAGChat(), "ShiftSummary": SystemPromptShiftSummary(),
		"AlertNoise": SystemPromptAlertNoise(), "LogPatterns": SystemPromptLogPatterns(),
		"Postmortem": SystemPromptPostmortem(), "RunbookUpdate": SystemPromptRunbookUpdate(),
		"NLToQuery": SystemPromptNLToQuery(), "CHQueryOptimize": SystemPromptCHQueryOptimize(),
		"ServiceTags": SystemPromptServiceTags(),
	}
}

// v0.8.374 — operator decision ("hepsi Türkçe"): every PROSE copilot
// surface carries the shared Turkish directive; the AI-analysis panel
// already had its own Turkish prompt, so Explain answering in English
// was the inconsistency. Strict-JSON surfaces are pinned to NOT carry
// it — a language directive invites prose around machine-parsed
// output (systemNLToQuery emits DSL JSON, systemCHQueryOptimize and
// systemServiceTags emit structured suggestions).
//
// v0.9.1128 — sicil üzerinden koşuyor; kapsam artık 15 değil 27 prompt
// (internal/api'den taşınan beşi dahil).
func TestProsePromptsAnswerInTurkish(t *testing.T) {
	texts := promptTexts()
	for name, class := range promptRegistry() {
		p := texts[name]
		switch class {
		case classDirective:
			if !strings.HasSuffix(p, AnswerInTurkish) {
				t.Errorf("%s must end with the Turkish directive", name)
			}
			if n := strings.Count(p, AnswerInTurkish); n != 1 {
				t.Errorf("%s dil direktifi %d kez geçiyor, 1 olmalı", name, n)
			}
		case classTurkishNative:
			if strings.HasSuffix(p, AnswerInTurkish) {
				t.Errorf("%s ortak direktifle bitiyor — sicilde classDirective olmalı", name)
			}
			// Türkçe-native olma iddiasının çıpası. Prompt İngilizceye
			// yeniden yazılırsa (kod-değiştirme vergisi geri gelir) burası
			// kırmızıya döner.
			if !strings.Contains(p, "Sen Coremetry") {
				t.Errorf("%s Türkçe-native sayılıyor ama Türkçe talimat çıpası yok", name)
			}
		case classStructured:
			if strings.Contains(p, "Türkçe") {
				t.Errorf("%s is a structured-output prompt and must NOT carry the language directive", name)
			}
		}
	}
}

// TestCodePromptsExtendBaseVerbatim — v0.9.831. Kod varyantı, kodsuz
// prompt'un ÜSTÜNE ek yapar; tabanı yeniden yazmaz.
//
// Neden pin: iki yüzey (kodlu/kodsuz) aynı soruyu cevaplıyor ve
// aralarındaki tek fark kod olmalı. Taban metin kopyalanıp
// ayrışırsa operatör aynı exception'da kutuyu işaretleyip
// işaretlememesine göre bambaşka bir cevap alır ve nedenini
// göremez.
func TestCodePromptsExtendBaseVerbatim(t *testing.T) {
	cases := []struct {
		name, body, code string
	}{
		{"exception", systemExceptionBody, systemExceptionCode},
		{"trace", systemTraceBody, systemTraceCode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.HasPrefix(tc.code, tc.body) {
				t.Fatalf("%s kod prompt'u tabandan başlamıyor", tc.name)
			}
			if !strings.Contains(tc.code, systemCodeAddendum) {
				t.Fatalf("%s kod prompt'unda kod eki yok", tc.name)
			}
			if !strings.HasSuffix(tc.code, AnswerInTurkish) {
				t.Fatalf("%s kod prompt'u dil direktifiyle bitmiyor", tc.name)
			}
			// Direktif TEK kez: gövde + ek + direktif, gövdenin kendi
			// direktifi değil.
			if n := strings.Count(tc.code, AnswerInTurkish); n != 1 {
				t.Fatalf("%s dil direktifi %d kez geçiyor, 1 olmalı", tc.name, n)
			}
		})
	}
	// Kodsuz prompt'lar kod ekini TAŞIMAMALI — kodsuz istekte
	// "gördüğün kod" talimatı vermek modele olmayan bir kanıt
	// vaat eder.
	for name, p := range map[string]string{"systemException": systemException, "systemTrace": systemTrace} {
		if strings.Contains(p, "KOD BAĞLAMI") {
			t.Errorf("%s kodsuz olmasına rağmen kod eki taşıyor", name)
		}
	}
}

// TestChatRoundCapExtendsBaseVerbatim — v0.9.1232. Tur-tavanı prompt'u
// serbest döngü prompt'unun ÜSTÜNE ek yapar; tabanı yeniden yazmaz.
//
// Neden pin: aynı istek içinde iki prompt gider (turlar için taban, son
// tur için tavanlı hâl) ve aralarındaki tek fark "artık tool çağırma"
// olmalı. Taban kopyalanıp ayrışırsa operatör, cevabın döngünün
// neresinde üretildiğine göre bambaşka bir posture ile karşılaşır ve
// nedenini göremez. İkinci assert, ekin ANLAMINI çiviler: metin
// Türkçeleştirilirken (v0.9.1232) "tool çağırma" talimatının düşmesi
// modeli tavandan sonra yeni tool çağırmaya iterdi — sağlayıcıya
// tools=nil gittiği için o çağrı sessizce boş cevap olurdu.
func TestChatRoundCapExtendsBaseVerbatim(t *testing.T) {
	cap := SystemPromptChatRoundCap()
	if !strings.HasPrefix(cap, SystemPromptChat()) {
		t.Fatal("tur-tavanı prompt'u serbest döngü tabanından başlamıyor")
	}
	if !strings.Contains(cap, "tool ÇAĞIRMA") {
		t.Error("tur-tavanı ekinde 'tool ÇAĞIRMA' talimatı yok")
	}
	if strings.Contains(SystemPromptChat(), "TUR TAVANI") {
		t.Error("taban prompt tur-tavanı ekini taşıyor — her tur 'hakkın bitti' der")
	}
}

// ─── Yapısal kapılar (Faz 1.6) ──────────────────────────────────────
//
// Neden davranış testi yetmiyor: internal/api'de İKİNCİ bir prompt
// açmak hiçbir davranış testini kırmaz. Beş prompt tam olarak böyle
// yaşadı — dil kapısı yeşil koştu, çünkü o prompt'ları hiç görmedi.
// Aşağıdaki kapılar SAHİPLİĞİ ölçer: prompt metni yalnız
// internal/copilot/prompts.go'da doğar.

var accessorRe = regexp.MustCompile(`(?m)^func SystemPrompt(\w+)\(\) string`)

func readGoFiles(t *testing.T, dir string, includeTests bool) map[string]string {
	t.Helper()
	out := map[string]string{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("%s okunamadı (%v) — kapı yolu bozulmuş, testin kendisi anlamsızlaşır", dir, err)
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if !includeTests && strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out[e.Name()] = string(raw)
	}
	if len(out) == 0 {
		// Tarayıcı hiçbir şey bulmazsa test SESSİZCE geçerdi.
		t.Fatalf("%s altında hiç .go taranmadı — tarayıcı bozuk", dir)
	}
	return out
}

// TestPromptRegistryCoversEveryAccessor — sicil ↔ accessor çift yönlü
// eşleşme. Yeni prompt eklenip sicile yazılmazsa (yani dil kapısından
// muaf kalırsa) burası kırmızıya döner; sicilde olup accessor'ı
// silinen ad da öyle.
func TestPromptRegistryCoversEveryAccessor(t *testing.T) {
	files := readGoFiles(t, ".", false)
	src, ok := files["prompts.go"]
	if !ok {
		t.Fatal("prompts.go yok — Faz 1.6 dosyası taşınmış/silinmiş")
	}
	found := map[string]bool{}
	for _, m := range accessorRe.FindAllStringSubmatch(src, -1) {
		found[m[1]] = true
	}
	if len(found) < 20 {
		t.Fatalf("prompts.go'da yalnız %d accessor bulundu — regex bayatlamış", len(found))
	}
	reg := promptRegistry()
	texts := promptTexts()
	for name := range found {
		if _, ok := reg[name]; !ok {
			t.Errorf("SystemPrompt%s sicilde YOK — yeni prompt dil kapısından muaf kalıyor "+
				"(promptRegistry'ye sınıfıyla ekle)", name)
		}
		if _, ok := texts[name]; !ok {
			t.Errorf("SystemPrompt%s promptTexts'te yok — metin denetlenmiyor", name)
		}
	}
	for name := range reg {
		if !found[name] {
			t.Errorf("sicilde SystemPrompt%s var ama prompts.go'da accessor'ı yok", name)
		}
	}
	// prompts.go DIŞINDA accessor tanımlanamaz: prompt sahipliği tek
	// dosyada durmalı, yoksa "dibe bak" sorunu isim değiştirip geri gelir.
	for name, other := range files {
		if name == "prompts.go" {
			continue
		}
		if accessorRe.MatchString(other) {
			t.Errorf("%s içinde SystemPrompt accessor'ı tanımlı — prompt'lar prompts.go'da yaşar", name)
		}
	}
}

// TestNoPromptsDeclaredInAPIPackage — Faz 1.6'nın silme sözü.
// internal/api artık prompt METNİ barındırmaz; yalnız accessor'ı çağırır
// ve çalışma-zamanı ekini (hitap ön-sözü, şema, kullanıcı bloğu) kendi
// tarafında kurar.
//
// Tarayıcı bilinçli DAR: (a) taşınan beş adın hiçbiri geri gelmemeli,
// (b) prompt açılış çıpaları ("Sen Coremetry", "You are a senior"…) ve
// paylaşılan dil direktifine yapılan referans non-test dosyalarda
// bulunmamalı. Test dosyaları kapsam dışı — greeting_test.go gibi
// yerlerde bu ifadeler ÖRNEK GİRDİ olarak geçiyor ve prompt değiller.
func TestNoPromptsDeclaredInAPIPackage(t *testing.T) {
	apiDir := filepath.Join("..", "api")

	// (a) Taşınan adlar — testler dahil hiçbir yerde.
	for name, src := range readGoFiles(t, apiDir, true) {
		for _, gone := range []string{
			"serviceAnalysisPrompt", "guidedChatPrompt", "drawerChatPrompt",
			"ragSystemPrompt", "chatSystemPrompt",
		} {
			if strings.Contains(src, gone) {
				t.Errorf("internal/api/%s: %s geri gelmiş — prompt sahipliği yine bölündü "+
					"(copilot.SystemPrompt… accessor'ını çağır)", name, gone)
			}
		}
	}

	// (b) Prompt-şekilli metin çıpaları — yalnız üretim dosyaları.
	anchors := []string{
		"Sen Coremetry",           // Türkçe-native prompt açılışı
		"You are a senior",        // İngilizce SRE prompt açılışı
		"You are Coremetry",       //  ” chat prompt açılışı
		"copilot.AnswerInTurkish", // paylaşılan direktifi api'de eklemek = api'de prompt kurmak
	}
	for name, src := range readGoFiles(t, apiDir, false) {
		for _, a := range anchors {
			if strings.Contains(src, a) {
				t.Errorf("internal/api/%s: %q geçiyor — sistem prompt'u internal/copilot/prompts.go'da "+
					"tanımlanır, api yalnız accessor çağırır", name, a)
			}
		}
	}
}
