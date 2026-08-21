package mcptools

// v0.9.1230 (AI perf) — KOMPAKT katalog görünümünün yapısal kapısı.
//
// Kayıt defteri TEK (mcptools.ToolList) ama iki tüketicisi var ve bağlam
// bütçeleri zıt:
//
//   - Dış MCP istemcisi (Claude Desktop, frontier model) kataloğu bir kez
//     okur ve uzun İngilizce sözleşmeden kazanç sağlar.
//   - Yerel gemma4 AYNI kataloğu HER TURDA yutar. Ölçüm (v0.9.1230):
//     33 tool = 24.268 B açıklama + 17.672 B şema = 41.940 B, serbest
//     döngü 5 tura kadar → tek bir soruda ~200 KB katalog.
//
// Çözüm ikinci bir liste DEĞİL, aynı literalde ikinci bir ALAN
// (mcp.Tool.ShortDescription). İkinci liste açılsaydı sürüklenme
// kaçınılmazdı; alan olduğunda yeni tool'un yazarı iki görünümü yan yana
// görür — ve bu dosya boş bırakmasına izin vermez.
//
// Kapı üç yönlü: (a) her tool'un kompakt metni VAR, (b) kompakt metin
// gerçekten KOMPAKT (yoksa alan tören olur), (c) MCP yüzeyi DEĞİŞMEDİ.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cilcenk/coremetry/internal/mcp"
)

// shortDescMaxBytes — tek bir kompakt açıklamanın tavanı.
//
// Amaç "kısa yaz" ricası değil, TAM METNİN yapıştırılmasını engellemek:
// katalogdaki en kısa tam açıklama 224 B, en uzunu 1814 B. 400 B tavanı
// iki-üç cümlelik bir sözleşmeye rahat yeter, bir paragrafa yetmez.
const shortDescMaxBytes = 400

// shortDescMinBytes — alt sınır. "Servisleri listeler" gibi bir metin
// alanı DOLDURUR ama modele hiçbir ayrım öğretmez; kompakt görünümün
// işi maliyeti düşürmek, kararı kötüleştirmek değil.
const shortDescMinBytes = 60

// shortCatalogMaxBytes — TOPLAM kompakt katalog bütçesi.
//
// Diyetin kendisi buradan ölçülür: tam açıklamalar 24.268 B idi. 9 KB
// tavanı bugünkü toplamın (~5.9 KB) üstünde makul bir pay bırakır ama
// katalog sessizce eski ağırlığına dönemez — 33 tool'un her biri 400 B
// tavanına dayansaydı 13 KB ederdi, yani tool başına tavan TEK BAŞINA
// yetmiyor.
const shortCatalogMaxBytes = 9000

// TAMLIK — her tool'un kompakt metni olmalı. Yeni bir tool kompakt
// açıklamasız gemiye giremez (mcp.Tool.ChatDescription() tam metne
// düşerdi: güvenli ama pahalı, ve sessiz).
func TestEveryToolHasShortDescription(t *testing.T) {
	tools := ToolList(Deps{})
	if len(tools) < 30 {
		t.Fatalf("katalog beklenmedik biçimde küçük (%d) — test yanlış şeyi ölçüyor olabilir", len(tools))
	}
	totalShort, totalFull := 0, 0
	for _, tl := range tools {
		totalShort += len(tl.ShortDescription)
		totalFull += len(tl.Description)
		if tl.ShortDescription == "" {
			t.Errorf("%s: ShortDescription BOŞ — sohbet yolu bu tool için her tur tam metni ödüyor", tl.Name)
			continue
		}
		if n := len(tl.ShortDescription); n > shortDescMaxBytes {
			t.Errorf("%s: kompakt açıklama %d B (tavan %d) — tam metin yapıştırılmış olabilir", tl.Name, n, shortDescMaxBytes)
		}
		if n := len(tl.ShortDescription); n < shortDescMinBytes {
			t.Errorf("%s: kompakt açıklama %d B (taban %d) — hangi soruyu cevapladığını ve komşusundan farkını söylemeli", tl.Name, n, shortDescMinBytes)
		}
		if len(tl.ShortDescription) >= len(tl.Description) {
			t.Errorf("%s: kompakt açıklama tam metinden kısa DEĞİL (%d ≥ %d)", tl.Name, len(tl.ShortDescription), len(tl.Description))
		}
		if !utf8.ValidString(tl.ShortDescription) {
			t.Errorf("%s: kompakt açıklama geçerli UTF-8 değil", tl.Name)
		}
		// ChatDescription() kompakt metni SEÇMELİ — alan var ama
		// okunmuyorsa kazanç sıfır.
		if got := tl.ChatDescription(); got != tl.ShortDescription {
			t.Errorf("%s: ChatDescription() kompakt metni döndürmüyor", tl.Name)
		}
	}
	if totalShort > shortCatalogMaxBytes {
		t.Errorf("kompakt katalog %d B — tavan %d B (diyet eriyor)", totalShort, shortCatalogMaxBytes)
	}
	// Diyetin ANLAMLI olması: kompakt görünüm tam metnin yarısından
	// küçük kalmalı, yoksa her tur ödenen bedel aynı sınıfta kalır.
	if totalShort*2 >= totalFull {
		t.Errorf("kompakt katalog (%d B) tam kataloğun (%d B) yarısından küçük değil", totalShort, totalFull)
	}
	t.Logf("katalog: %d tool, tam %d B → kompakt %d B", len(tools), totalFull, totalShort)
}

// ChatDescription() BOŞKEN tam metne düşer — güvenli yön. Tamlık kapısı
// yukarıda; burada yalnız yedek dalın doğru davrandığı pinleniyor.
func TestChatDescriptionFallsBackToFull(t *testing.T) {
	full := mcp.Tool{Name: "x", Description: "tam metin"}
	if got := full.ChatDescription(); got != "tam metin" {
		t.Fatalf("boş ShortDescription tam metne düşmeli, %q döndü", got)
	}
	short := mcp.Tool{Name: "x", Description: "tam metin", ShortDescription: "kısa"}
	if got := short.ChatDescription(); got != "kısa" {
		t.Fatalf("dolu ShortDescription seçilmeli, %q döndü", got)
	}
}

// MCP YÜZEYİ DEĞİŞMEDİ — dış istemci tools/list'te TAM İngilizce
// sözleşmeyi görmeye devam eder. Kompakt metin protokole SIZMAMALI:
// sızarsa Claude Desktop sınıfı bir istemci maliyet şerhlerini,
// dürüstlük negatiflerini ve "şunu uydurma" kurallarını kaybeder.
func TestMCPToolsListServesFullDescriptions(t *testing.T) {
	srv := mcp.New("coremetry-test", "v0.0.0-test")
	tools := ToolList(Deps{})
	for _, tl := range tools {
		srv.RegisterTool(tl)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/mcp", srv.HandleStreamable)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	resp, err := http.Post(ts.URL+"/api/mcp", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Result.Tools) != len(tools) {
		t.Fatalf("tools/list %d tool döndü, kayıt defterinde %d var", len(out.Result.Tools), len(tools))
	}
	byName := map[string]mcp.Tool{}
	for _, tl := range tools {
		byName[tl.Name] = tl
	}
	for _, got := range out.Result.Tools {
		want, ok := byName[got.Name]
		if !ok {
			t.Errorf("tools/list bilinmeyen tool döndü: %s", got.Name)
			continue
		}
		if got.Description != want.Description {
			t.Errorf("%s: tools/list TAM açıklamayı servis etmiyor", got.Name)
		}
		if got.Description == want.ShortDescription {
			t.Errorf("%s: kompakt metin MCP protokolüne SIZDI — dış istemci sözleşmeyi kaybeder", got.Name)
		}
	}
}

// Kompakt metinler ÜRÜNÜN dilinde (Türkçe) yazılır — sohbet yüzeyi
// Türkçe ve modele iki dilli katalog vermek küçük modelde ek yük.
// Kapı kaba ama ısırır: her metin en az bir Türkçe'ye özgü harf ya da
// Türkçe bir bağlaç taşımalı.
func TestShortDescriptionsAreTurkish(t *testing.T) {
	markers := []string{"ı", "ş", "ğ", "ü", "ö", "ç", "İ", " ile ", " için ", " ve "}
	for _, tl := range ToolList(Deps{}) {
		hit := false
		for _, m := range markers {
			if strings.Contains(tl.ShortDescription, m) {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("%s: kompakt açıklama Türkçe görünmüyor: %q", tl.Name, tl.ShortDescription)
		}
	}
}
