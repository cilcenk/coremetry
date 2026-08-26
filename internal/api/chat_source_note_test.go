package api

import (
	"os"
	"strings"
	"testing"
)

// v0.10.29 — Copilot denetimi: serbest tool döngüsünde sunucu-taraflı
// "Kaynak:" künyesi YOKTU. Guided, drawer ve RAG üçünde vardı; modelin
// EN SERBEST olduğu ve en çok uydurabileceği yolda yoktu.
//
// Operatör iki kademeyi ayırt edemiyordu: birinde cevabın altında kaynak
// yazıyor, diğerinde hiçbir şey. Dipnotun YOKLUĞU "kaynak yok" değil
// "kaynak bilinmiyor" anlamına geliyordu.

func TestChatSourceNote(t *testing.T) {
	t.Run("araçlar listelenir, sıra korunur", func(t *testing.T) {
		got := chatSourceNoteTR([]string{"list_services", "get_trace"})
		if !strings.Contains(got, "list_services, get_trace") {
			t.Errorf("araç sırası korunmamış: %q", got)
		}
		if !strings.Contains(got, "(2 araç)") {
			t.Errorf("sayı yok: %q", got)
		}
	})

	// Sıra bir BİLGİ: modelin izlediği yol soruşturmanın şeklini anlatıyor
	// (önce list_services, sonra get_trace…). Alfabetik sıralamak onu siler.
	t.Run("alfabetik sıralanmaz", func(t *testing.T) {
		got := chatSourceNoteTR([]string{"zzz_tool", "aaa_tool"})
		if strings.Index(got, "aaa_tool") < strings.Index(got, "zzz_tool") {
			t.Errorf("sıra bozulmuş — çağrı sırası bilgisi kayboldu: %q", got)
		}
	})

	t.Run("tekrar eden araç bir kez sayılır", func(t *testing.T) {
		// Aynı araç birden çok turda çağrılabiliyor; iki kez yazmak
		// sayıyı da yanlış gösterirdi.
		got := chatSourceNoteTR([]string{"search_logs", "search_logs", "get_trace", "search_logs"})
		if !strings.Contains(got, "(2 araç)") {
			t.Errorf("tekilleştirme yok: %q", got)
		}
		if strings.Count(got, "search_logs") != 1 {
			t.Errorf("araç adı tekrarlanmış: %q", got)
		}
	})

	t.Run("uzun liste kırpılır", func(t *testing.T) {
		many := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
		got := chatSourceNoteTR(many)
		if !strings.Contains(got, "+2") {
			t.Errorf("kırpma ilan edilmemiş: %q", got)
		}
		// Sayı KIRPILMAMIŞ toplamı göstermeli.
		if !strings.Contains(got, "(8 araç)") {
			t.Errorf("toplam kırpılmış listeden sayılmış: %q", got)
		}
	})

	// ⚠ EN ÖNEMLİ DAL — denetimin B3 bulgusu.
	//
	// Serbest döngüde model 0 tool çağırıp doğrudan cevap yazabiliyor ve
	// o metin `answer` olarak yayınlanıyor. Arayüzde bunu telemetriden
	// gelen cevaptan ayıran hiçbir işaret YOKTU.
	t.Run("SIFIR araç — cevabın canlı veriye dayanmadığı SÖYLENİR", func(t *testing.T) {
		for _, in := range [][]string{nil, {}, {""}, {"  "}} {
			got := chatSourceNoteTR(in)
			if !strings.Contains(got, "hiçbir telemetri aracı çağrılmadı") {
				t.Errorf("sıfır araçta uyarı yok (%v): %q", in, got)
			}
			if !strings.Contains(got, "canlı veriye değil") {
				t.Errorf("ayrım açıkça söylenmiyor (%v): %q", in, got)
			}
			// Uydurma bir araç adı ÜRETİLMEMELİ.
			if strings.Contains(got, "araç)") {
				t.Errorf("sıfır araçta sayı künyesi basılmış (%v): %q", in, got)
			}
		}
	})
}

func TestDedupePreserveOrder(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		want []string
	}{
		{"boş", nil, nil},
		{"tekrar yok", []string{"a", "b"}, []string{"a", "b"}},
		{"tekrar var", []string{"a", "b", "a"}, []string{"a", "b"}},
		{"boş dizgeler elenir", []string{"", "a", "  ", "b"}, []string{"a", "b"}},
		{"hepsi boş", []string{"", "  "}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := dedupePreserveOrder(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("uzunluk %d; %d bekleniyordu (%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %q; %q bekleniyordu", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestChatWiresSourceNote — KABLOLAMA PİNİ.
func TestChatWiresSourceNote(t *testing.T) {
	b, err := os.ReadFile("copilot_chat.go")
	if err != nil {
		t.Fatalf("copilot_chat.go okunamadı: %v", err)
	}
	src := stripGoCommentsAPI(string(b))

	for _, must := range []string{
		"var calledTools []string",
		"calledTools = append(calledTools, tc.Name)",
		"chatSourceNoteTR(calledTools)",
	} {
		if !strings.Contains(src, must) {
			t.Errorf("künye kablolanmamış, kayıp: %s", must)
		}
	}
	// Künye ⚙ çipiyle AYNI AD KAYNAĞINI kullanmalı: ikisi de `tc.Name`.
	// Ayrı bir ad üretirlerse cevabın altındaki atıf ile üstündeki çipler
	// farklı şeyler söyler.
	//
	// ⚠ v0.10.53 — bu kontrol eskiden `iAppend < iChip` konum karşılaştırması
	// yapıyordu. O bir VEKİLDİ ve yanlış şeyi koruyordu: künye kaydının
	// çipten önce gelmesi, kaydın `byName` kontrolünden de ÖNCE gelmesi
	// demekti — yani modelin UYDURDUĞU araç adı künyeye "Kaynak:" diye
	// giriyordu. Vekil, düzeltmenin kendisini engelliyordu.
	//
	// Çip ile künyenin ÜYELİĞİ artık bilerek ayrı: çip DENENEN her aracı
	// gösteriyor (operatör modelin ne denediğini görmeli), künye ise yalnız
	// VERİ DÖNDÜRENİ. Korunması gereken şey ad kaynağının tekliği.
	if !strings.Contains(src, "emitStepChip(emit, tc.Name") {
		t.Error("çip artık tc.Name basmıyor — künyeyle ad kaynağı ayrışmış olabilir")
	}
	if !strings.Contains(src, "calledTools = append(calledTools, tc.Name)") {
		t.Error("künye artık tc.Name kaydetmiyor — çiple ad kaynağı ayrışmış olabilir")
	}
}

// TestSourceNoteCountsOnlyToolsThatReturnedData — v0.10.53.
//
// v0.10.29 künyeyi "cevap CANLI VERİYE dayanıyor mu" sorusuna cevap
// olsun diye ekledi. Ama kayıt satırı `byName` kontrolünden ÖNCE
// duruyordu: modelin UYDURDUĞU, var olmayan bir araç adı künyeye
// "Kaynak:" diye giriyordu. Hata dönen araç da öyle.
//
// ⚠ İkisi de künyenin İDDİASINI çürütüyor. Çalışmamış bir araç veri
// döndürmez; hata metni telemetri değildir. Uydurma bir adı kaynak diye
// göstermek, uydurmayı KANITLA süslemek olurdu — künyenin var olma
// sebebinin tam tersi.
//
// Kayıt AST'de tek yerde olmalı: başarı dalında. Bu kapı onu çiviliyor.
func TestSourceNoteCountsOnlyToolsThatReturnedData(t *testing.T) {
	src := readSourceFile(t, "copilot_chat.go")

	n := strings.Count(src, "calledTools = append(calledTools, tc.Name)")
	if n != 1 {
		t.Fatalf("calledTools %d yerde yazılıyor, 1 olmalı — birden fazla kayıt "+
			"yeri, başarısız dalın da künyeye sızması demek", n)
	}
	// Kayıt, bilinmeyen-araç dalından SONRA gelmeli.
	unknown := strings.Index(src, `msg := fmt.Sprintf("unknown tool %q", tc.Name)`)
	record := strings.Index(src, "calledTools = append(calledTools, tc.Name)")
	if unknown < 0 {
		t.Fatal("bilinmeyen-araç dalı bulunamadı — test bayatlamış")
	}
	if record < unknown {
		t.Error("künye kaydı bilinmeyen-araç dalından ÖNCE: modelin uydurduğu " +
			"araç adı 'Kaynak:' diye gösterilir")
	}
	// Ve hata dalından SONRA: `tr.Content = out` yalnız başarıda çalışır.
	success := strings.Index(src, "tr.Content = out")
	if success < 0 {
		t.Fatal("başarı dalı bulunamadı — test bayatlamış")
	}
	if record < success {
		t.Error("künye kaydı başarı dalından ÖNCE: hata dönen araç da kaynak sayılır")
	}
}
