package api

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/ai/provider"
)

// explain_cache.go — ✨ EXPLAIN CEVABI ÖNBELLEĞİ (v0.10.83, operatör
// ürün kararı: "chatte explain dediğimde dönen cevabı kaydet, tekrar
// tekrar LLM'e sorgu atma. Redis'te tut, gerekirse 1 saat TTL ile").
//
// ── NEDEN BU DİKİŞ ──────────────────────────────────────────────────────
//
// Dokuz Explain yüzeyinin tamamı deliverExplain'den geçiyor; önbellek
// oraya bir anahtar parametresiyle girdi ve imza değişikliği dokuz çağrı
// yerini DERLEYİCİYE yürüttü (v0.10.50'nin aynı taktiği: paralel bir yol
// açmak, bir yerin unutulmasına davetiyedir).
//
// ── ANAHTAR: PROMPT'UN KENDİSİ ──────────────────────────────────────────
//
// Anahtar (system, user, ekstra kimlik) üçlüsünün FNV'si — yani modele
// gerçekten giden metnin tamamı (v0.5.187 sert kuralı: anahtar TÜM
// girdileri hash'ler). Trace'e yeni span/log gelirse user değişir, anahtar
// değişir, taze LLM çağrısı çıkar — bayatlık kendi kendini düzeltir.
// "Kodu da incele" yolunda kod bloğu da kimliğe girer: kodlu ve kodsuz
// cevap ayrı satırlardır.
//
// ── DÜRÜSTLÜK ───────────────────────────────────────────────────────────
//
//   • İsabet ETİKETLENİR: cevaba cached:true + cachedAtMs biner ve arayüz
//     yaşıyla gösterir. Operatör taze sanmasın.
//   • İsabette exchangeId SAKLANAN kimliktir: 👍/👎 gerçek LLM çağrısının
//     ai_calls satırına bağlanır. Bu isteğin kendi xid'i kullanılsaydı
//     geri bildirim hiçliğe düşerdi.
//   • Kurtarılmış-düşünce cevabı (⚠ işaretli, v0.10.37/66) SAKLANMAZ:
//     modelin çalışma notunu bir saat boyunca "cevap" diye çoğaltmak,
//     işaretin kapattığı kusuru önbellekten geri getirirdi.
//   • "Yeniden sor" ?refresh=1 ile önbelleği ATLAR: operatör açıkça taze
//     istiyor; ona isabet servis etmek düğmeyi işlevsiz bırakırdı.
//     Taze cevap eskisinin ÜZERİNE yazılır.
//
// Redis yoksa cache.Noop her Get'te ıska verir — davranış bugünkünün
// aynısı, hiçbir yol önbelleğe muhtaç değil.

// explainCacheTTL — operatör kararı: 1 saat.
const explainCacheTTL = time.Hour

// explainCacheEnvelope — saklanan zarf.
type explainCacheEnvelope struct {
	Text string `json:"t"`
	AtMs int64  `json:"at"`
	// Xid — cevabı üreten GERÇEK LLM çağrısının exchange kimliği.
	Xid string `json:"xid"`
}

// explainCacheKey — SAF. Üç parça da hash'e girer; ayraç \x00 (metin
// içinde geçemez), yani ("ab","c") ile ("a","bc") çakışmaz.
func explainCacheKey(system, user, extra string) string {
	h := fnv.New128a()
	h.Write([]byte(system))
	h.Write([]byte{0})
	h.Write([]byte(user))
	h.Write([]byte{0})
	h.Write([]byte(extra))
	return fmt.Sprintf("explain:%x", h.Sum(nil))
}

// explainCacheGet — isabette zarfı döndürür. refresh=1 isteği ıska sayar.
func (s *Server) explainCacheGet(r *http.Request, key string) (explainCacheEnvelope, bool) {
	if key == "" || s.cache == nil || r.URL.Query().Get("refresh") == "1" {
		return explainCacheEnvelope{}, false
	}
	raw, ok, err := s.cache.Get(r.Context(), key)
	if err != nil || !ok || len(raw) == 0 {
		return explainCacheEnvelope{}, false
	}
	var env explainCacheEnvelope
	if json.Unmarshal(raw, &env) != nil || strings.TrimSpace(env.Text) == "" {
		return explainCacheEnvelope{}, false
	}
	return env, true
}

// explainCacheSet — temiz cevabı saklar. Hata sessizce yutulur: önbellek
// bir hızlandırıcı, cevabın ön koşulu değil.
func (s *Server) explainCacheSet(ctx context.Context, key, text, xid string) {
	if key == "" || s.cache == nil || strings.TrimSpace(text) == "" {
		return
	}
	if provider.IsSalvagedThinking(text) {
		return // çalışma notu bir saat boyunca çoğaltılmaz
	}
	env := explainCacheEnvelope{Text: text, AtMs: time.Now().UnixMilli(), Xid: xid}
	if raw, err := json.Marshal(env); err == nil {
		_ = s.cache.Set(ctx, key, raw, explainCacheTTL)
	}
}
