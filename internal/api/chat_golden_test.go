package api

import (
	"strings"
	"testing"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// chat_golden_test.go — v0.10.44. SOHBET BORU HATTININ ALTIN KÜMESİ.
//
// Copilot denetiminin 12. ve son maddesi: "yukarıdaki 11 kusuru
// düzeltince düzeldiğini ölçecek araç yok — uzun vadede en pahalı
// eksik."
//
// ── NE ÖLÇER, NE ÖLÇMEZ ─────────────────────────────────────────────────
//
// `internal/anomaly/prompt_golden_test.go`nun sohbet karşılığı ve aynı
// ayrımı taşıyor:
//
//	ÖLÇER: modele GİDEN girdiyi ve operatöre ÇIKAN sözleşmeleri —
//	  bağlam önsözü, çıpa ilanı, kaynak künyesi, kırpma dürüstlüğü,
//	  hata cümlelerinin eyleme dönüklüğü. Hepsi deterministik; model
//	  çağrısı GEREKTİRMEZ, CI'da koşar.
//
//	ÖLÇMEZ: modelin çıktı kalitesini. Onun için insan değerlendirmesi ya
//	  da canlı koşum gerekir.
//
// Yani bu küme "gemma4 iyi cevap veriyor mu" demez; "ona iyi bir soru
// sorduk ve cevabını dürüstçe çerçeveledik mi" der.
//
// ── NEDEN SENARYO, NEDEN BİRİM DEĞİL ────────────────────────────────────
//
// Bu gece düzeltilen sekiz kusurun her birinin kendi birim testi VAR.
// Ama hepsi AYRI dosyalarda ve hiçbiri diğerinin varlığını bilmiyor:
// biri sessizce gerilerse öbürleri yeşil kalır ve boru hattının BÜTÜN
// olarak dürüst kaldığını hiçbir şey söylemez.
//
// Bu dosya o bütünü tek yerde tutuyor. Bir senaryo kırmızıya dönerse,
// hangi sözleşmenin gerilediği adıyla yazılı.

// chatScenario — gerçekçi bir sohbet durumu ve ondan beklenen
// DÜRÜSTLÜK özellikleri.
type chatScenario struct {
	name string
	// build — senaryonun modele/operatöre ürettiği metni kurar.
	build func() string
	// wantIn / wantNotIn — altın küme iddiaları.
	wantIn    []string
	wantNotIn []string
	// why — bu senaryonun hangi kusuru koruduğu. Kırmızıya dönünce
	// okunacak cümle bu.
	why string
}

func TestChatPipelineGolden(t *testing.T) {
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)

	scenarios := []chatScenario{
		{
			name: "ekran bağlamı — servis + 6 saatlik pencere",
			why: "v0.10.32: serbest döngü bağlam-kördü. Ekranda checkout açık ve " +
				"6 saatlik pencere seçiliyken soru FİLO GENELİNE ve 30 DAKİKAYA gidiyordu.",
			build: func() string {
				return screenContextPreambleTR(ChatScreenContext{
					Service: "checkout-service", Env: "prod", RangeS: 21600,
				})
			},
			wantIn: []string{
				"checkout-service",
				"range_s=21600",
				"1800 YERİNE", // prompt'un kendi varsayılanı EZİLMELİ
				"AKSİNİ SÖYLEMEDİKÇE",
			},
		},
		{
			name: "geçmişe zoom — çıpa ilan edilir",
			why: "v0.10.33: mutlak pencere süreye çökertiliyor ve sunucu şimdiye " +
				"yeniden çapalıyordu. Dün geceye zoom yapan operatör BUGÜNKÜ veriyi alıyordu.",
			build: func() string {
				anchor := time.Date(2026, 8, 25, 4, 0, 0, 0, time.UTC)
				return screenContextPreambleTR(ChatScreenContext{
					Service: "svc-a", RangeS: 3600, AnchorTo: anchor, Anchored: true,
				})
			},
			wantIn: []string{"GEÇMİŞE sabitlenmiş", "2026-08-25 04:00", "DEĞİL"},
		},
		{
			name: "göreli pencere — çıpa ilan EDİLMEZ",
			why: "v0.10.33: göreli bir pencereyi mutlakmış gibi ilan etmek, " +
				"düzeltmenin kendi ürettiği yeni bir yanlış olurdu.",
			build: func() string {
				return screenContextPreambleTR(ChatScreenContext{
					Service: "svc-a", RangeS: 3600, AnchorTo: now, Anchored: false,
				})
			},
			wantNotIn: []string{"GEÇMİŞE sabitlenmiş"},
		},
		{
			name: "araç çağrılmadı — cevap canlı veriye DAYANMIYOR",
			why: "v0.10.29 + denetim B3: serbest döngüde model 0 tool çağırıp " +
				"doğrudan cevap yazabiliyor ve arayüzde bunu telemetriden gelen " +
				"cevaptan ayıran hiçbir işaret YOKTU.",
			build:  func() string { return chatSourceNoteTR(nil) },
			wantIn: []string{"hiçbir telemetri aracı çağrılmadı", "canlı veriye değil"},
		},
		{
			name: "araç çağrıldı — künye sırayı korur",
			why: "v0.10.29: modelin izlediği yol soruşturmanın şeklini anlatıyor; " +
				"alfabetik sıralamak o bilgiyi siler.",
			build: func() string {
				return chatSourceNoteTR([]string{"list_services", "get_trace", "search_logs"})
			},
			wantIn:    []string{"list_services, get_trace, search_logs", "(3 araç)"},
			wantNotIn: []string{"get_trace, list_services"},
		},
		{
			name: "problem kanıtı KIRPILMIŞ — toplam LIMIT değil",
			why: "v0.10.21: sunucu LIMIT'in uzunluğunu 'toplam' diye veriyordu. " +
				"47 problemli serviste modele 'toplam 10' gidiyordu ve model " +
				"kurallara uyarak yanlış sayıyı sadakatle aktarıyordu.",
			build: func() string {
				probs := make([]chstore.Problem, 10)
				for i := range probs {
					probs[i] = chstore.Problem{
						ID: "p", RuleName: "hata oranı", Severity: "critical",
						Service: "checkout-service", Priority: "P1",
						StartedAt: now.Add(-time.Hour).UnixNano(),
					}
				}
				return renderProblemsEvidenceTR(probs, "checkout-service", "", now,
					problemsTotal{n: 47, known: true})
			},
			wantIn: []string{
				"toplam 47",
				"dağılım yalnız gösterilen", // 1+1+0 ≠ 47 çelişkisi ilan edilir
				"10 satır gösteriliyor",
			},
			wantNotIn: []string{"toplam 10"},
		},
		{
			name: "toplam ÖLÇÜLEMEDİ — kesin sayı iddia edilmez",
			why: "v0.10.21: CountProblems, ListProblems'ın Services dilimini " +
				"bilmiyor. O yolda sayım yapmak YENİ bir yalan olurdu.",
			build: func() string {
				probs := []chstore.Problem{{
					ID: "p1", RuleName: "r", Severity: "warning", Service: "a",
					StartedAt: now.UnixNano(),
				}}
				return renderProblemsEvidenceTR(probs, "", "", now, problemsTotal{})
			},
			wantIn:    []string{"en az 1", "ölçülemedi"},
			wantNotIn: []string{": toplam"},
		},
		{
			name: "alışveriş tavana dayandı — model suçlanmaz",
			why: "v0.10.24: ham `context deadline exceeded` metnini operatör " +
				"'model zaman aşımına uğradı' diye okur ve MODELİ suçlar; oysa " +
				"olan ALIŞVERİŞİN tavana dayanmasıdır ve farklı bir eylem gerektirir.",
			build:     func() string { return chatDeadlineMessageTR(540 * time.Second) },
			wantIn:    []string{"9 dakika", "tavan", "dar"},
			wantNotIn: []string{"context", "deadline"},
		},
		{
			name: "bağlam taştı — küçültme denendiği SÖYLENİR",
			why: "v0.10.26: isContextOverflowErr yazılıydı ama sohbete kablolu " +
				"değildi; operatör ham İngilizce sağlayıcı gövdesi görüyordu.",
			build:     func() string { return chatOverflowMessageTR(true) },
			wantIn:    []string{"bağlam penceresine sığmadı", "yarıya indirilip", "daralt"},
			wantNotIn: []string{"context length"},
		},
		{
			name: "model kendi grafiğini yazdı — CANLI çizilmez",
			why: "v0.10.47: sunucu çiti render_chart'ın DOĞRULANMIŞ spec'inden " +
				"kuruluyor, ama modelin kendi metnine yazdığı ```chart``` çiti " +
				"arayüzde ondan ayırt edilemiyordu — uydurma bir kapsam CANLI " +
				"grafik olarak çiziliyor ve grafik düzyazıdan yüksek güven taşıyor.",
			build: func() string {
				out, _ := stripModelChartFences(
					"Şuna bak:\n```chart\n{\"service\":\"olmayan-servis\",\"agg\":\"p99\"}\n```\n")
				return out
			},
			wantIn:    []string{"çizilmedi", "yalnız sunucu kurar"},
			wantNotIn: []string{"olmayan-servis", "```chart"},
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			got := sc.build()
			for _, w := range sc.wantIn {
				if !strings.Contains(got, w) {
					t.Errorf("KAYIP %q\n\nKORUDUĞU KUSUR: %s\n\nÜRETİLEN:\n%s", w, sc.why, got)
				}
			}
			for _, w := range sc.wantNotIn {
				if strings.Contains(got, w) {
					t.Errorf("OLMAMASI GEREKEN %q VAR\n\nKORUDUĞU KUSUR: %s\n\nÜRETİLEN:\n%s", w, sc.why, got)
				}
			}
		})
	}
}

// TestGoldenCorpusCoversTonightsFixes — KÜMENİN KENDİSİ ÇÜRÜMESİN.
//
// Altın küme ancak KAPSADIĞI kadar değerli. Bir senaryo silinirse ya da
// küme sessizce küçülürse, koruduğu sözleşme de korumasız kalır ve bunu
// hiçbir şey söylemez — kümenin erimesi, kusurun geri gelmesinden daha
// sinsi çünkü test sayısı hâlâ yeşil görünür.
func TestGoldenCorpusCoversTonightsFixes(t *testing.T) {
	// Her biri v0.10.2x-4x'te düzeltilmiş BİR kusur sınıfı.
	const minScenarios = 10
	// Kümedeki senaryo sayısını saymak için testi yeniden koşmak yerine
	// kaynağı tarıyoruz: sayım, kümenin KENDİ yapısına bağlı kalmalı.
	src := readSourceFile(t, "chat_golden_test.go")
	n := strings.Count(src, "\t\t\tname: \"")
	if n < minScenarios {
		t.Errorf("altın kümede %d senaryo var, en az %d bekleniyordu — "+
			"küme erimiş olabilir ve erimesi SESSİZDİR (test sayısı yeşil kalır)",
			n, minScenarios)
	}
	// Her senaryo NİÇİN var olduğunu yazmalı: kırmızıya dönen bir altın
	// test, gerekçesi olmadan "güncelle geç" refleksiyle bastırılır.
	if strings.Count(src, "why:") < minScenarios {
		t.Error("bazı senaryolar `why` taşımıyor — gerekçesiz bir altın test, " +
			"kırmızıya döndüğünde düşünülmeden güncellenir")
	}
}
