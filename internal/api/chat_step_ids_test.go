package api

// chat_step_ids_test.go — v0.9.1229.
//
// Guided yol adım çiplerini `i` TAŞIMADAN yayınlıyordu (map[string]string
// {"tool","args"}) ve eşli `step-result` hiç yoktu; frontend `i` yoksa
// detayı düşürdüğü için ASIL cevap yolunun çipleri tıklanamayan ölü
// etiketlerdi. Burada iki şey pinleniyor:
//
//	(a) kimlik üretimi ve kanıt yayını (saf seam),
//	(b) guided bundle'larda her adımın eşli bir kanıt yayını olduğu
//	    (kaynak taraması — saf test tek başına bağlanma kanıtı değil).

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/mcp"
)

// recEmit — yayınlanan olayları sırayla toplayan sahte emit.
type recEv struct {
	kind    string
	payload map[string]any
}

func recEmit(out *[]recEv) func(string, any) {
	return func(kind string, payload any) {
		m := map[string]any{}
		switch p := payload.(type) {
		case map[string]any:
			for k, v := range p {
				m[k] = v
			}
		case map[string]string:
			for k, v := range p {
				m[k] = v
			}
		}
		*out = append(*out, recEv{kind: kind, payload: m})
	}
}

// Kimlik TEK sayaçtan gelmeli ve akıştaki HER `step` olayını
// numaralamalı — yazılışı (map[string]any / map[string]string) fark
// etmeksizin. Numarasız bir çip, frontend'in çip şeridi ile detay
// dizisini kaydırır ve kanıt YANLIŞ çipe yapışır.
func TestWithStepIDsNumbersEveryStepOnce(t *testing.T) {
	var got []recEv
	emit := withStepIDs(recEmit(&got))

	if i := emitStepChip(emit, "list_problems", `{"service":"a"}`); i != 1 {
		t.Errorf("ilk çip kimliği = %d, beklenen 1", i)
	}
	emit("delta", map[string]string{"text": "arada"}) // step DEĞİL: sayaç ilerlemez
	emit("step", map[string]string{"tool": "bağlam: ekrandaki trace (abc)"})
	if i := emitStepChip(emit, "service_context", ""); i != 3 {
		t.Errorf("üçüncü çip kimliği = %d, beklenen 3", i)
	}

	var ids []int
	for _, e := range got {
		if e.kind != "step" {
			continue
		}
		n, ok := e.payload["i"].(int)
		if !ok {
			t.Fatalf("`i` taşımayan step olayı: %#v", e.payload)
		}
		ids = append(ids, n)
	}
	if len(ids) != 3 {
		t.Fatalf("step olayı sayısı = %d, beklenen 3", len(ids))
	}
	for i, n := range ids {
		if n != i+1 {
			t.Errorf("kimlikler monotonik değil: %v", ids)
		}
	}
	// map[string]string payload dönüştürülürken alanları KAYBETMEMELİ.
	if got[2].payload["tool"] != "bağlam: ekrandaki trace (abc)" {
		t.Errorf("bağlam çipinin etiketi kayboldu: %#v", got[2].payload)
	}
}

// Sarmalanmamış emit'te (iç içe bundle çağrılarının no-op emit'i) kimlik
// 0'dır ve kanıt SESSİZCE düşer: numarasız `step-result` hiçbir çiple
// eşleşmez.
func TestEmitStepChipUnwrappedYieldsNoEvidence(t *testing.T) {
	var got []recEv
	emit := recEmit(&got)
	i := emitStepChip(emit, "db_summary", "")
	if i != 0 {
		t.Errorf("sarmalanmamış emit kimliği = %d, beklenen 0", i)
	}
	emitStepEvidence(emit, i, "db_summary", "kanıt", nil)
	for _, e := range got {
		if e.kind == "step-result" {
			t.Errorf("eşleşemeyecek step-result yayınlandı: %#v", e.payload)
		}
	}
}

func TestEmitStepEvidenceShape(t *testing.T) {
	big := strings.Repeat("ö", chatStepPreviewMax) // 2 bayt/rune → tavanın 2 katı
	cases := []struct {
		name          string
		text          string
		err           error
		wantEmitted   bool
		wantOK        bool
		wantTruncated bool
		wantBytes     int
		wantPrefix    string
	}{
		{name: "düz kanıt", text: "Açık problem yok.\n", wantEmitted: true, wantOK: true,
			wantBytes: len("Açık problem yok.\n"), wantPrefix: "Açık problem yok."},
		// v0.9.1234 — hata çipi artık ham sürücü metni değil, tool
		// hatalarının ortak sözleşmesi (mcp.ToolErrorJSON): sınıf +
		// retryable + Türkçe ipucu + kırpılmış detay. Pin literal
		// metne DEĞİL sözleşmeye bakıyor; ipucu cümlesi ayarlanınca
		// testin kırılması gürültü olurdu.
		{name: "hata", text: "", err: errors.New("code: 159, Timeout exceeded"), wantEmitted: true,
			wantBytes:  len(mcp.ToolErrorJSON(errors.New("code: 159, Timeout exceeded"))),
			wantPrefix: `{"error":"timeout","retryable":true`},
		// Boş kanıt çipi düğmeye çevirirdi ve açılan blok bomboş olurdu:
		// ölü affordance, eksik affordance'tan kötü (v0.9.592 dersi).
		{name: "boş metin", text: "   \n", wantEmitted: false},
		{name: "kırpma", text: big, wantEmitted: true, wantOK: true, wantTruncated: true,
			wantBytes: len(big)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []recEv
			emit := withStepIDs(recEmit(&got))
			i := emitStepChip(emit, "tool_x", "")
			emitStepEvidence(emit, i, "tool_x", c.text, c.err)
			var res *recEv
			for k := range got {
				if got[k].kind == "step-result" {
					res = &got[k]
				}
			}
			if !c.wantEmitted {
				if res != nil {
					t.Fatalf("kanıt yayınlanmamalıydı: %#v", res.payload)
				}
				return
			}
			if res == nil {
				t.Fatal("step-result yayınlanmadı")
			}
			if res.payload["i"] != i {
				t.Errorf("kimlik eşleşmiyor: %v != %d", res.payload["i"], i)
			}
			if res.payload["ok"] != c.wantOK {
				t.Errorf("ok = %v, beklenen %v", res.payload["ok"], c.wantOK)
			}
			if res.payload["truncated"] != c.wantTruncated {
				t.Errorf("truncated = %v, beklenen %v", res.payload["truncated"], c.wantTruncated)
			}
			if res.payload["bytes"] != c.wantBytes {
				t.Errorf("bytes = %v, beklenen %d (kırpılmamış GERÇEK boy)", res.payload["bytes"], c.wantBytes)
			}
			prev, _ := res.payload["preview"].(string)
			if !strings.HasPrefix(prev, c.wantPrefix) {
				t.Errorf("önizleme %q ile başlamıyor: %q", c.wantPrefix, prev)
			}
			if len(prev) > chatStepPreviewMax {
				t.Errorf("önizleme tavanı aştı: %d bayt", len(prev))
			}
		})
	}
}

// ── bağlanma pini ───────────────────────────────────────────────────
//
// Guided bundle'larda AÇILAN her ⚙ adımının eşli bir kanıt yayını
// olmalı. Saf test bunu göremez: emitGuidedStepResult çağrısını silmek
// (a) derlenir, (b) hiçbir birim testi kırmaz, (c) çipi sessizce
// v0.9.1181 öncesine — ölü etikete — geri döndürür.
// Sayım DEĞİL, AD eşlemesi: bir dosyada hata dalları yüzünden kanıt
// yayını adım sayısından fazla olabiliyor, o yüzden salt sayı kıyası
// yeni bir eşsiz çipi göremezdi. Kapı her tool ADI için ayrı sorar.
func TestGuidedStepsHavePairedEvidence(t *testing.T) {
	stepRe := regexp.MustCompile(`emitGuidedStep\(emit, "([^"]+)"`)
	resRe := regexp.MustCompile(`emitGuidedStepResult\(emit, [^,]+, "([^"]+)"`)
	files := []string{
		"copilot_guided.go", "copilot_deps.go", "copilot_pods.go",
		"copilot_shift.go", "copilot_drawer.go",
	}
	total := 0
	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			src := stepSourceNoComments(t, f)
			paired := map[string]bool{}
			for _, m := range resRe.FindAllStringSubmatch(src, -1) {
				paired[m[1]] = true
			}
			steps := stepRe.FindAllStringSubmatch(src, -1)
			for _, m := range steps {
				total++
				if !paired[m[1]] {
					t.Errorf("%s: %q adım çipinin eşli kanıt yayını yok — eşsiz çip ölü "+
						"etikettir (v0.9.1181 öncesi hâl). Kanıt okuması OLMAYAN bağlam "+
						"çipleri emitGuidedContextStep kullanmalı.", f, m[1])
				}
			}
			// Değişken adlı çipler (drawer: emitGuidedStep(emit, step, ""))
			// regex'e girmez — o dosyalarda en azından bir kanıt yayını
			// DURMALI, yoksa kapı sessizce boş geçerdi.
			if !strings.Contains(src, "emitGuidedStepResult(emit,") {
				t.Errorf("%s: hiç kanıt yayını yok — çipler taşındıysa bu pini GÜNCELLE", f)
			}
		})
	}
	// v0.9.1229'da 34 literal adlı çip vardı; sayı düşerse çipler
	// sessizce kayboluyor demektir (kapının kapsamı erimesin).
	if total < 30 {
		t.Errorf("literal adlı adım çipi sayısı %d'e düştü (v0.9.1229: 34) — kapı kapsamı eriyor", total)
	}
}

// Bağlam çipleri (okuma YOK) ayrı bir yazılışta durmalı: ham
// emit("step", …) geri sızarsa hem kimliği sarmalayıcıya bırakan tek
// yol bozulur hem de yukarıdaki sayım kapısı o çipi "eşsiz" sanmaz.
func TestNoRawStepEmitsOutsideStepIDLayer(t *testing.T) {
	files := []string{
		"copilot_guided.go", "copilot_deps.go", "copilot_pods.go",
		"copilot_shift.go", "copilot_drawer.go", "rag.go",
	}
	for _, f := range files {
		if src := stepSourceNoComments(t, f); strings.Contains(src, `emit("step"`) {
			t.Errorf("%s: ham emit(\"step\", …) — emitGuidedStep / emitGuidedContextStep kullan", f)
		}
	}
}

// stepSourceNoComments — yorum satırlarını AYIKLAR: gerekçe yorumları
// aranan dizeleri (emit("step" gibi) içeriyor ve ayıklamayan bir
// tarayıcı bu depoda KÖR koştu.
func stepSourceNoComments(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("%s okunamadı: %v", name, err)
	}
	var sb strings.Builder
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return sb.String()
}

// v0.10.161 — çip kökeni: guided ön-yüklemeleri "guided" taşır, model araç
// çağrısı taşımaz. Frontend rozeti bu alandan okur, delta'dan çıkarmaz.
func TestStepChipOrigin(t *testing.T) {
	var got []recEv
	emit := withStepIDs(recEmit(&got))
	emitGuidedStep(emit, "list_problems", `{"service":"a"}`)
	emitGuidedContextStep(emit, "bağlam: ekrandaki trace (abc)")
	emitStepChip(emit, "search_traces", `{}`)
	if len(got) != 3 {
		t.Fatalf("3 olay bekleniyordu, %d", len(got))
	}
	if o, _ := got[0].payload["origin"].(string); o != "guided" {
		t.Errorf("guided çip origin=%q, beklenen guided", o)
	}
	if o, _ := got[1].payload["origin"].(string); o != "guided" {
		t.Errorf("bağlam çipi origin=%q, beklenen guided", o)
	}
	if _, has := got[2].payload["origin"]; has {
		t.Errorf("model araç çipi origin taşımamalı: %v", got[2].payload)
	}
}

// v0.10.161 — tool döngüsündeki step-result durationMs taşır (kaynak
// sözleşmesi: alan ölçümün hemen ardından stepEv'e yazılır; saf seam yok,
// bu yüzden bağlanma kaynak taramasıyla pinlenir — "test edilmiş ama
// ulaşılamaz" sınıfına düşmesin).
func TestToolLoopStepResultCarriesDuration(t *testing.T) {
	src, err := os.ReadFile("copilot_chat.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, `"durationMs": toolDur.Milliseconds(),`) {
		t.Fatal("copilot_chat.go: step-result stepEv durationMs taşımıyor")
	}
	if !regexp.MustCompile(`toolT0 := time\.Now\(\)\s*\n\s*out, herr := runChatTool\(`).MatchString(s) {
		t.Fatal("copilot_chat.go: süre ölçümü runChatTool çağrısını sarmalamıyor")
	}
}
