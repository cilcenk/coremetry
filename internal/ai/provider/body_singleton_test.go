// body_singleton_test.go — FAZ 1.3'ün YAPISAL kapısı.
//
// Faz 1.2 buffered explain gövdelerini tek yazılışa indirdi ve o sözü
// salvage_singleton_test.go'daki TestBufferedBuildersAreGone koruyor.
// Faz 1.3 aynı sözü STREAM ve TOOL-CALLING gövdelerine genişletiyor:
// artık internal/ altında LLM istek gövdesi kuran tek yer
// internal/ai/provider.
//
// Neden davranış testi yetmiyor (Faz 1.2 ile aynı gerekçe): ikinci bir
// gövde üreticisi EKLEMEK hiçbir davranış testini kırmaz — iki üretici
// bugün aynı şeyi yapar, sonra biri düzeltilir öteki unutulur. 1024
// max_tokens paritesi tam böyle ~1000 sürüm yaşadı (v0.9.1120) ve
// üçüncü sitesi TAM OLARAK stream.go'ydu.
//
// Tarayıcı YORUMLARI SOYAR. Bu şart: bu dosyaların yorumları
// `"stream_options"`, `/chat/completions` gibi deyimleri anlatmak için
// bolca ANIYOR; ham metin taraması ya sürekli kırmızı yanar ya da
// (daha kötüsü) kapı gevşetilerek kör bırakılır.
package provider

import (
	"strings"
	"testing"
)

// stripGoComments, Go kaynağından yorumları atar; dizgi sabitlerine
// DOKUNMAZ.
//
// Üç tuzak bilinçli olarak ele alınmıştır (naif bir regex üçünde de
// yanılır):
//   - `"http://x"` içindeki `//` yorum DEĞİLDİR,
//   - `//` yorumunun içindeki `/*` blok AÇMAZ,
//   - ham dizgi (`…`) içindeki her şey veridir.
//
// Satır sayısını korumak için yorumların yerine boşluk/satırsonu
// bırakılır.
func stripGoComments(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	const (
		code = iota
		lineComment
		blockComment
		str
		rawStr
		runeLit
	)
	state := code
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch state {
		case code:
			if c == '/' && i+1 < len(src) {
				if src[i+1] == '/' {
					state, i = lineComment, i+1
					continue
				}
				if src[i+1] == '*' {
					state, i = blockComment, i+1
					continue
				}
			}
			switch c {
			case '"':
				state = str
			case '`':
				state = rawStr
			case '\'':
				state = runeLit
			}
			out.WriteByte(c)
		case lineComment:
			if c == '\n' {
				state = code
				out.WriteByte(c)
			}
		case blockComment:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				state, i = code, i+1
				continue
			}
			if c == '\n' {
				out.WriteByte(c)
			}
		case str:
			out.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				i++
				out.WriteByte(src[i])
				continue
			}
			if c == '"' {
				state = code
			}
		case rawStr:
			out.WriteByte(c)
			if c == '`' {
				state = code
			}
		case runeLit:
			out.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				i++
				out.WriteByte(src[i])
				continue
			}
			if c == '\'' {
				state = code
			}
		}
	}
	return out.String()
}

// TestStripGoComments — KAPININ KENDİSİNİN testi. Soyucu bozulursa kapı
// sessizce kör koşar (frontend tarafında zLayers kapısı bu yüzden 69
// sürüm boyunca hiçbir şey ölçmeden yeşil yandı).
func TestStripGoComments(t *testing.T) {
	tests := []struct {
		name           string
		src            string
		wantContains   []string
		wantNotContain []string
	}{
		{
			name:           "satır yorumu atılır",
			src:            "x := 1 // \"stream_options\" burada anlatılıyor\ny := 2\n",
			wantContains:   []string{"x := 1", "y := 2"},
			wantNotContain: []string{"stream_options"},
		},
		{
			name:           "blok yorumu atılır",
			src:            "a()\n/* \"tool_choice\" tartışması */\nb()\n",
			wantContains:   []string{"a()", "b()"},
			wantNotContain: []string{"tool_choice"},
		},
		{
			name:         "dizgi içindeki // yorum DEĞİLDİR",
			src:          "url := \"https://api.anthropic.com/v1/messages\"\n",
			wantContains: []string{"https://api.anthropic.com/v1/messages"},
		},
		{
			name:           "satır yorumu içindeki /* blok AÇMAZ",
			src:            "// bak /* buraya\nkod := \"stream\"\n",
			wantContains:   []string{`kod := "stream"`},
			wantNotContain: []string{"buraya"},
		},
		{
			name:         "ham dizgi içindeki yorum işaretleri veridir",
			src:          "q := `SELECT 1 -- x /* y */ // z`\n",
			wantContains: []string{"SELECT 1 -- x /* y */ // z"},
		},
		{
			name:         "kaçışlı tırnak dizgiyi kapatmaz",
			src:          "s := \"a\\\"// b\"\nt := 1\n",
			wantContains: []string{"t := 1", `a\"// b`},
		},
		{
			name:         "rune literali dizgi açmaz",
			src:          "c := '\"'\nd := \"tool_use\"\n",
			wantContains: []string{`d := "tool_use"`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := stripGoComments(tc.src)
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("soyucu kodu yedi: %q içinde %q yok", got, want)
				}
			}
			for _, bad := range tc.wantNotContain {
				if strings.Contains(got, bad) {
					t.Errorf("soyucu yorumu bırakmış: %q içinde %q var", got, bad)
				}
			}
		})
	}
}

// llmBodyIdioms — bir LLM istek gövdesi/ucu kurulduğunu ELE VEREN
// deyimler. Bilinçli olarak DAR seçildi:
//
//   - `"tools"` ve `"messages"` YOK: internal/mcp bunları MCP protokol
//     yanıtlarında kullanıyor ve o alakasız çakışma kapıyı ya kırmızı
//     tutar ya da gevşetilmesine yol açar.
//   - Uçlar TAM URL: `api.anthropic.com` tek başına
//     chstore/external_catalogue.go'daki alan-adı kataloğuyla çakışırdı.
// llmBodyIdiom — bir deyimi ele veren YAZILIŞLAR. Çoğunun tek yazılışı
// var; `stream` bayrağının iki yazılışı olabilir (map anahtarı ya da
// struct etiketi) ve çıplak `"stream"` araması bir HTTP SORGU
// PARAMETRESİ okuyan alakasız dosyaya da çarpıyordu (v0.9.1127, Faz 1.5:
// explain uçlarının `?stream=1` bayrağı — LLM gövdesi değil).
//
// Sinyal daraltıldı, kapı GEVŞETİLMEDİ: iki gövde yazılışı da ölçülüyor
// ve TestLLMBodyIdiomSpellingsBite ikisini de mutasyonla pinliyor. Bu,
// yukarıdaki yorumun `"tools"`/`"messages"` için verdiği kararın aynısı —
// alakasız çakışma kapıyı ya kırmızı tutar ya da gevşetilmesine yol açar,
// doğru cevap deyimi keskinleştirmektir.
type llmBodyIdiom struct {
	label     string
	spellings []string
}

func oneSpelling(s string) llmBodyIdiom { return llmBodyIdiom{label: s, spellings: []string{s}} }

var llmBodyIdioms = []llmBodyIdiom{
	// stream:true bayrağı — ÜÇ yazılış: map literali (`"stream":`), map
	// indeksleme (`m["stream"] = true`) ve struct etiketi. Üçü de gövde
	// kurar; biri atlanırsa kapının kapsamı sessizce daralır.
	{label: `"stream" (gövde bayrağı)`, spellings: []string{
		`"stream":`, `"stream"]`, `json:"stream"`, "json:\"stream,",
	}},
	oneSpelling(`"stream_options"`), // include_usage — düşerse usage sessizce 0'lanır
	oneSpelling(`"tool_calls"`),
	oneSpelling(`"tool_use"`),
	oneSpelling(`"tool_result"`),
	oneSpelling(`"/chat/completions"`),
	oneSpelling(`api.anthropic.com/v1/messages`),
	// Faz 1.4 — son bant-dışı LLM istemcisi (rag.embedOnce) kapandı.
	// Tırnaklı yazılış şart: rag.go yorumları `/v1/embeddings`i düz
	// metinde anıyor ve tarayıcı yorumları zaten soyuyor, ama uç
	// birleştirmesi (`… + "/embeddings"`) yalnız bu şekilde ele veriyor.
	oneSpelling(`"/embeddings"`),
}

// containsAny — deyimin yazılışlarından HERHANGİ biri geçiyor mu?
func containsAny(src string, spellings []string) bool {
	for _, sp := range spellings {
		if strings.Contains(src, sp) {
			return true
		}
	}
	return false
}

// futureLLMBodyIdioms — bugün HİÇBİR YERDE geçmeyen, ama geçtiği gün
// provider'da geçmesi gereken deyimler. Taban denetimi bilerek
// UYGULANMAZ: `tool_choice`'ı Coremetry bugüne kadar hiç göndermedi
// (tool seçimi modele bırakılıyor), yani "provider'da da yok" DOĞRU
// hâldir. Kapının işi burada ileriye dönük: biri tool zorlaması
// eklerse copilot'ta değil provider'da eklesin.
var futureLLMBodyIdioms = []string{
	`"tool_choice"`,
}

// TestLLMRequestBodiesLiveOnlyInProvider — Faz 1.3'ün silme sözü.
func TestLLMRequestBodiesLiveOnlyInProvider(t *testing.T) {
	files := scanNonTestGoFiles(t)
	const providerDir = "internal/ai/provider/"

	for _, idiom := range llmBodyIdioms {
		var inProvider, outside []string
		for path, src := range files {
			if !containsAny(stripGoComments(src), idiom.spellings) {
				continue
			}
			if strings.Contains(path, providerDir) {
				inProvider = append(inProvider, path)
				continue
			}
			outside = append(outside, path)
		}
		// TABAN: deyim provider'da HİÇ geçmiyorsa anahtar bayatlamıştır
		// (alan adı değişmiş, yazılış kaymış) ve kapı "dışarıda 0 hit"
		// diye SESSİZCE yeşil yanardı — ölçmediği için.
		if len(inProvider) == 0 {
			t.Errorf("%s deyimi internal/ai/provider'da HİÇ geçmiyor — kapı anahtarı bayat, bu satır hiçbir şey ölçmüyor", idiom.label)
		}
		if len(outside) > 0 {
			t.Errorf("%s deyimi provider DIŞINDA kuruluyor: %v — LLM istek gövdeleri tek yazılış olmalı (Faz 1.3)", idiom.label, outside)
		}
	}

	// Kapı bugün gerçekten ısırıyor mu? Deyimlerin provider'daki tabanı
	// yukarıda ölçülüyor; burada DIŞARIDA bir gövde kurulsa yakalanır mı
	// sorusunu sentetik kaynakla soruyoruz. Yazılış daraltmasının
	// (v0.9.1127) kapının kapsamını düşürmediğinin pini bu.
	for _, tc := range []struct {
		name string
		src  string
		want bool
	}{
		{"map anahtarı", "body := map[string]any{\n\"stream\": true,\n}\n", true},
		{"map indekslemesi", "body[\"stream\"] = true\n", true},
		{"struct etiketi", "type req struct{ Stream bool `json:\"stream\"` }\n", true},
		{"struct etiketi + omitempty", "type req struct{ Stream bool `json:\"stream,omitempty\"` }\n", true},
		// HTTP sorgu parametresi bir LLM gövdesi DEĞİL — v0.9.1127'de
		// explain uçlarına gelen `?stream=1` bayrağı bu yüzden serbest.
		{"sorgu parametresi okuması", "v := r.URL.Query().Get(\"stream\")\n", false},
		{"yorumda geçen kelime", "// stream:true bayrağı\n", false},
	} {
		got := containsAny(stripGoComments(tc.src), llmBodyIdioms[0].spellings)
		if got != tc.want {
			t.Errorf("stream deyimi / %s: eşleşme=%v, beklenen %v", tc.name, got, tc.want)
		}
	}

	for _, idiom := range futureLLMBodyIdioms {
		for path, src := range files {
			if strings.Contains(path, providerDir) {
				continue
			}
			if strings.Contains(stripGoComments(src), idiom) {
				t.Errorf("%s deyimi provider DIŞINDA kuruluyor: %s — tool zorlaması da tek transport'tan geçmeli", idiom, path)
			}
		}
	}
}

// TestStreamAndToolBuildersAreGone — Faz 1.3'te silinen yapılar.
// TestBufferedBuildersAreGone'un (Faz 1.2) ikizi: bu adların geri
// gelmesi, gövde/çözümleme mantığının copilot'a geri sızdığı demektir.
func TestStreamAndToolBuildersAreGone(t *testing.T) {
	files := scanNonTestGoFiles(t)
	for _, name := range []string{
		"openAIChatTransport",    // uç + header çözümü → provider
		"openAIStreamAccum",      // SSE biriktiriciler → provider
		"anthropicStreamAccum",   //
		"scanSSE",                // SSE hattı → provider
		"sseDataPayload",         //
		"classifyStreamResponse", // küçük-harfli ikiz (artık ClassifyStreamResponse)
	} {
		for path, src := range files {
			if strings.Contains(path, "internal/ai/provider/") {
				continue
			}
			stripped := stripGoComments(src)
			if strings.Contains(stripped, "func "+name+"(") ||
				strings.Contains(stripped, ") "+name+"(") ||
				strings.Contains(stripped, "type "+name+" ") {
				t.Errorf("%s hâlâ provider DIŞINDA tanımlı: %s", name, path)
			}
		}
	}
}
