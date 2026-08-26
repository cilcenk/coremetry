package api

import (
	"strings"
	"testing"
)

// chat_chart_origin_test.go — v0.10.47.
//
// Copilot denetiminin son maddesi: bir ```chart``` çitinin KÖKENİ arayüzde
// ayırt edilmiyordu. Sunucu çitleri doğrulanmış spec'ten kuruluyor ve
// cevabın SONUNA ekleniyor; modelin kendi metnine yazdığı bir çit ise
// UYDURMA bir kapsamla canlı grafik çizdiriyordu.
//
// Doğru veri + yanlış kapsam, düzyazıdan daha ikna edici bir hatadır.

func TestStripModelChartFences(t *testing.T) {
	for _, tc := range []struct {
		name      string
		in        string
		wantStrip int
		wantIn    []string
		wantNotIn []string
		why       string
	}{
		{
			name:      "modelin yazdığı chart çiti sökülür",
			in:        "Şuna bak:\n```chart\n{\"service\":\"uydurma-servis\",\"agg\":\"p99\"}\n```\nGörüldüğü gibi.",
			wantStrip: 1,
			wantIn:    []string{"Şuna bak:", "çizilmedi", "Görüldüğü gibi."},
			wantNotIn: []string{"uydurma-servis", "```chart"},
			why:       "asıl kusur: uydurma kapsam CANLI grafik olarak çiziliyordu",
		},
		{
			name:      "sessizce silinmez — yerine görünür not gelir",
			in:        "```chart\n{}\n```",
			wantStrip: 1,
			wantIn:    []string{"⚠", "grafik çizmeye çalıştı"},
			why: "sessiz silme, düzyazıdaki 'aşağıdaki grafikte' cümlesini " +
				"sahipsiz bırakırdı",
		},
		{
			name:      "chart OLMAYAN çit aynen korunur",
			in:        "```sql\nSELECT 1\n```",
			wantStrip: 0,
			wantIn:    []string{"```sql", "SELECT 1"},
			why:       "sökme yalnız chart'a ait; kod bloğu modelin meşru çıktısı",
		},
		{
			name: "kod bloğunun İÇİNDEKİ chart metni çit sanılmaz",
			// ⚠ Bu dal olmasaydı, modelin ```chart biçimini AÇIKLADIĞI bir
			// örnek sessizce bozulurdu.
			in:        "```json\n```chart\n```\nson",
			wantStrip: 0,
			wantIn:    []string{"```json"},
			why:       "iç içe görünen çit, arayüzde de kapanış sayılıyor",
		},
		{
			name:      "dil eki taşıyan chart çiti de sökülür",
			in:        "```chart json\n{}\n```",
			wantStrip: 1,
			wantNotIn: []string{"```chart"},
			why:       "arayüz ilk sözcüğe bakıyor: `split(/\\s+/)[0]`",
		},
		{
			name:      "BÜYÜK harfli chart çiti de sökülür",
			in:        "```CHART\n{}\n```",
			wantStrip: 1,
			wantNotIn: []string{"```CHART"},
			why:       "arayüz toLowerCase() uyguluyor — büyük harf kaçış deliği olurdu",
		},
		{
			name:      "3 boşluk girintili çit sökülür",
			in:        "   ```chart\n{}\n   ```",
			wantStrip: 1,
			wantNotIn: []string{"chart\n{}"},
			why:       "arayüzün FENCE kuralı `^\\s{0,3}` — 3 boşluk hâlâ çit",
		},
		{
			name: "4 boşluk girintili çit SÖKÜLMEZ",
			// Arayüz de onu çit saymıyor; sökmek meşru metni bozardı.
			in:        "    ```chart\n{}\n    ```",
			wantStrip: 0,
			wantIn:    []string{"```chart"},
			why:       "sunucu arayüzden DAHA GENİŞ eşleşirse meşru metni bozar",
		},
		{
			name:      "kapanmamış chart çiti de sökülür",
			in:        "metin\n```chart\n{\"service\":\"x\"",
			wantStrip: 1,
			wantIn:    []string{"metin"},
			wantNotIn: []string{"```chart", "\"service\""},
			why:       "akış yarıda kesilirse arayüz bloğu AÇIK çizer — yine canlı",
		},
		{
			name:      "dilsiz çit panik etmez",
			in:        "```\nkod\n```",
			wantStrip: 0,
			wantIn:    []string{"kod"},
			why:       "Fields boş dilim döner; indekslemek panik ederdi",
		},
		{
			name:      "çitsiz metin aynen döner",
			in:        "checkout p99 340ms.",
			wantStrip: 0,
			wantIn:    []string{"checkout p99 340ms."},
			why:       "en sık yol; dokunulmamalı",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, n := stripModelChartFences(tc.in)
			if n != tc.wantStrip {
				t.Errorf("sökülen çit = %d; beklenen %d\n\nGEREKÇE: %s\n\nÜRETİLEN:\n%s",
					n, tc.wantStrip, tc.why, got)
			}
			for _, w := range tc.wantIn {
				if !strings.Contains(got, w) {
					t.Errorf("KAYIP %q\n\nGEREKÇE: %s\n\nÜRETİLEN:\n%s", w, tc.why, got)
				}
			}
			for _, w := range tc.wantNotIn {
				if strings.Contains(got, w) {
					t.Errorf("OLMAMASI GEREKEN %q VAR\n\nGEREKÇE: %s\n\nÜRETİLEN:\n%s", w, tc.why, got)
				}
			}
		})
	}
}

// TestFenceRuleMatchesFrontend — İKİ YAZIMIN AYNI KALMASI SÖZLEŞME.
//
// Sunucunun çit tanımı arayüzünkinden DAR olursa sökülmemiş bir çit yine
// canlı çizilir (kusur geri gelir); GENİŞ olursa sunucu meşru bir kod
// bloğunu boşuna söker. İkisi de sessiz.
//
// Bu, deponun tekrar eden sınıfı: [[feedback-gate-single-spelling]] —
// kuralın ikinci yazımı ilkinden habersiz kayar.
func TestFenceRuleMatchesFrontend(t *testing.T) {
	src := readSourceFile(t, "../../frontend/src/components/ai/chatMarkdown.ts")

	// Arayüzün çit kuralı. Değişirse bu test kızarır ve isFenceLine'ın
	// da güncellenmesi gerektiği ADIYLA yazılı olur.
	if !strings.Contains(src, "const FENCE = /^\\s{0,3}```/") {
		t.Error("arayüzün FENCE kuralı değişmiş — isFenceLine (chat_chart_origin.go) " +
			"onun İKİZİ; biri kayarsa model-yazımı çit ya sökülmeden canlı çizilir " +
			"ya da meşru kod bloğu boşuna sökülür")
	}
	// Dil çıkarımı: ilk sözcük + küçük harf.
	if !strings.Contains(src, "split(/\\s+/)[0].toLowerCase()") {
		t.Error("arayüzün dil çıkarımı değişmiş — fenceLang onun İKİZİ")
	}
	// Arayüz hâlâ SALT dile bakarak canlı grafik çiziyor mu? Bir gün köken
	// işareti eklenirse bu sökme gereksizleşir ve gerekçesi yeniden
	// okunmalı.
	bubble := readSourceFile(t, "../../frontend/src/components/ai/ChatBubble.tsx")
	if !strings.Contains(bubble, "b.lang === 'chart'") {
		t.Error("arayüz artık salt dile bakmıyor olabilir — sökmenin gerekçesi " +
			"(chat_chart_origin.go başlığı) yeniden okunmalı")
	}
}

// TestStripRunsBeforeTheEarlyReturn — MUHAFIZ ULAŞILABİLİR OLMALI.
//
// appendCharts, sunucu grafiği YOKSA erken dönüyor. Sökme o dönüşün
// ALTINA konsaydı, tam da en tehlikeli hâlde — model çit yazdı, sunucu
// hiç grafik üretmedi — hiç çalışmazdı: yeşil test, ulaşılamaz muhafız
// ([[feedback-tested-but-unreachable]], v0.9.1334).
func TestStripRunsBeforeTheEarlyReturn(t *testing.T) {
	src := readSourceFile(t, "copilot_chat.go")
	strip := strings.Index(src, "text, _ = stripModelChartFences(text)")
	if strip < 0 {
		t.Fatal("appendCharts model çitini sökmüyor — uydurma kapsam canlı çizilir")
	}
	early := strings.Index(src, "if len(chartBlocks) == 0 {")
	if early < 0 {
		t.Fatal("erken dönüş bulunamadı — test bayatlamış, elle doğrula")
	}
	if strip > early {
		t.Error("sökme erken dönüşün ALTINDA: sunucu grafiği olmayan turda hiç " +
			"çalışmaz ve o tur model çitinin TEK BAŞINA çizildiği turdur")
	}
}
