// v0.9.556 regresyon testi — anti-uydurma kurallarının kapsamı.
//
// Bulunuş: RCA verdict tasarımının çekişmeli incelemesi, önerilen
// kanıt kataloğunun "bulunamadı" kayıtlarını da alıntılanabilir hale
// getirdiğini gösterdi. Kontrol edince aynı açığın MEVCUT kodda da
// olduğu ortaya çıktı — ama tasarımda değil, kapsamda.
//
// systemProblem'in ÜÇ tüketicisi var:
//
//  1. arka plan ProblemExplainer (anomaly/problem_explainer.go:212)
//  2. operatör tıklaması /api/copilot/explain-problem (api/api.go:8417)
//  3. MCP explain_problem prompt'u (mcptools/prompts.go:82) — DIŞ istemciler
//
// Anti-uydurma kuralları yalnız (1)'in KULLANICI prompt'unda vardı ve
// orada da yalnız derin soruşturma kanıtı toplanmışsa. Yani (2) ve (3)
// korumasızdı — tam da bir insanın cevabı okuyup aksiyon aldığı yollar.
//
// Bu test kuralların SİSTEM prompt'unda kalmasını sabitler. Sistemde
// durdukları sürece üç tüketici de kapsanır; kullanıcı prompt'una geri
// taşınırlarsa iki yol sessizce korumasız kalır.
package copilot

import (
	"strings"
	"testing"
)

func TestSystemProblemCarriesAntiFabricationRules(t *testing.T) {
	p := SystemPromptProblem()

	cases := []struct {
		want string
		why  string
	}{
		{
			// EN ÖNEMLİSİ. Mevcut "veride olmayan … UYDURMA" kuralı bunu
			// kapsamıyor: bir sinyalin "bulunamadı" kaydı VERİLEN
			// veridir. Onu sebep diye göstermek kuralın harfine uyup
			// ruhunu çiğner.
			want: "bulunamadı",
			why: "negatif kanıt kuralı yok — model 'pod restart kaydı bulunamadı' " +
				"satırını 'pod restart döngüsünde' iddiasının KANITI olarak " +
				"gösterebilir ve mevcut kurallar bunu yakalamaz",
		},
		{
			want: "SEBEP olarak",
			why:  "negatif sinyalin sebep olarak gösterilmesini yasaklayan cümle kayıp",
		},
		{
			want: "kanıt yetersiz",
			why: "kaçış kapısı adıyla verilmemiş — modele 'sebep uydurma' demek " +
				"yetmez, YERİNE NE DİYECEĞİNİ söylemek gerekir; küçük model " +
				"boşluğu doldurmaya eğilimlidir",
		},
		{
			want: "hangi sinyallere bakıldığını",
			why: "kanıt yokken 'neye bakıldı' talimatı kayıp — o olmadan " +
				"'kanıt yetersiz' cevabı operatör için eyleme dönüşmez",
		},
	}

	for _, c := range cases {
		if !strings.Contains(p, c.want) {
			t.Errorf("systemProblem %q içermiyor: %s", c.want, c.why)
		}
	}
}

// TestAntiFabricationRulesNotDuplicatedInBody — kural sistem prompt'unda
// TEK kez geçmeli. İki kez yazılması küçük modelde bölüm sayımını
// bozar ve few-shot'ın ağırlığını seyreltir (aynı gerekçe
// TestSystemProblemPrompt'un tek-few-shot ve tek-dil-direktifi
// sayımlarında da var).
// v0.10.194 — genel bilgi cevabı (systemGeneralChat) SIFIR kanıtla konuşan
// tek yüzey; güvenlik hikâyesinin tamamı iki cümle: operatörün sistemi
// hakkında UYDURMA + ortam sorusunda "erişemediğini söyle" (küçük modele
// ne DEMESİ gerektiğini söyleyen yarı, yük taşıyan yarıdır).
func TestSystemGeneralChatCarriesAntiFabricationRules(t *testing.T) {
	p := SystemPromptGeneralChat()
	for _, want := range []string{"UYDURMA", "erişemediğini söyle", "EŞLEŞMEDİ", answerInTurkishLine} {
		if !strings.Contains(p, want) {
			t.Errorf("GeneralChat %q cümlesini taşımıyor", want)
		}
	}
}

func TestAntiFabricationRulesNotDuplicatedInBody(t *testing.T) {
	p := SystemPromptProblem()
	for _, phrase := range []string{"kanıt yetersiz", "SEBEP olarak"} {
		if got := strings.Count(p, phrase); got != 1 {
			t.Errorf("systemProblem %q ifadesini %d kez taşıyor, beklenen 1", phrase, got)
		}
	}
}

// v0.10.403 — CoSRE denetimi P1: üç explain prompt'unda kanıt sınırı yoktu
// (systemRunbook "name the actual dashboard/command" diye somut ad
// istiyor, systemIncident "blast radius" tahmini). Kardeşi systemTrace'in
// "Use ONLY facts present in the evidence" kuralı üçüne de taşındı.
func TestExplainPromptsCarryEvidenceBoundary(t *testing.T) {
	for name, p := range map[string]string{
		"incident": SystemPromptIncident(),
		"anomaly":  SystemPromptAnomaly(),
		"runbook":  SystemPromptRunbook(),
	} {
		for _, want := range []string{"Evidence boundary", "ONLY", "kanıt yetersiz", "Never\ninvent"} {
			if !strings.Contains(p, want) {
				t.Errorf("%s prompt'u %q içermiyor — kanıt sınırı kayıp", name, want)
			}
		}
	}
}

// v0.10.406 — CoSRE denetimi P5: 17 niyet + 5 slot yalnız tarifle
// verilmişti; küçük model şekli görmeli (kardeş prompt'ların few-shot
// gerekçesi). Özellikle `none` örneği: yanlış niyet none'dan kötüdür.
func TestIntentPromptCarriesExamples(t *testing.T) {
	p := SystemPromptIntentClassify()
	for _, want := range []string{"Örnekler", `"intent":"none"`, `"intent":"service_health","service":"checkout"`, `"rangeS":3600`} {
		if !strings.Contains(p, want) {
			t.Errorf("niyet prompt'u %q içermiyor", want)
		}
	}
}
