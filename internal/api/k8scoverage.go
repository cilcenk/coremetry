package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/cilcenk/coremetry/internal/auth"
)

// k8scoverage.go — K8s bağlam kapsama kartı (v0.10.36, entity katmanı Faz 0).
//
// ── NE ÖLÇÜYOR ──────────────────────────────────────────────────────────
//
// Filonun hangi kısmı hangi k8s resource alanını YAYIYOR. Bugün bu soru
// cevapsız: prod'da tek bir span'in resource seti görüldü ve ondan
// "namespace yok, node var, uid yok" çıkarıldı — ama bu TEK SPAN.
//
// ── NEDEN İLK DİLİM BU ──────────────────────────────────────────────────
//
// Entity katmanının asıl adımı (k8sattributes + RBAC) prod'da collector
// restart'ı istiyor ve collector pod bounce'ta wedge oluyor. O riski
// ölçülmemiş gerekçeyle almak yanlış sıra. Bu uç:
//
//   - hiçbir şeyi beklemiyor (yeni tablo/MV/kolon YOK)
//   - collector'a ve ingest'e DOKUNMUYOR
//   - tek commit'le geri alınır
//   - ve sonraki fazın KABUL TESTİ olur: değişiklikten önce/sonra aynı tablo
//
// ── DÜRÜSTLÜK ───────────────────────────────────────────────────────────
//
// Sonuç bir ÖRNEKLEM (bkz. chstore/k8s_coverage.go). Zarf örneklem tavanını
// ve servis başına görülen satır sayısını taşıyor ki operatör "0 gördüm"
// ile "alan gerçekten yok"u ayırabilsin. Bir kapsama kartının kendisi
// yanıltıcı olursa, ölçmek için var olduğu şeyi bozar.

// k8sCoverageRungs — pencere BASAMAKLARI.
//
// Serbest saniye değeri cache anahtarının kardinalitesini patlatır
// (v0.8.270 dersi): her tık yeni anahtar → cache hiç tutmaz. Basamaklar
// ayrıca taramayı da öngörülebilir tutuyor.
var k8sCoverageRungs = []int64{900, 3600, 21600, 86400}

// snapK8sCoverageRange — istenen pencereyi en yakın ÜST basamağa oturtur.
//
// Üste yuvarlama bilinçli: alta yuvarlamak operatörün istediğinden DAR bir
// pencere ölçmek olurdu ve seyrek yayan bir servis örneklemden düşerdi —
// yani kartın yanlış "alan yok" demesi.
func snapK8sCoverageRange(sec int64) int64 {
	if sec <= 0 {
		return k8sCoverageRungs[1] // varsayılan 1 saat
	}
	for _, r := range k8sCoverageRungs {
		if sec <= r {
			return r
		}
	}
	return k8sCoverageRungs[len(k8sCoverageRungs)-1]
}

// registerK8sCoverageRoutes — /api-route sözleşmesi: uç KENDİ dosyasında,
// api.go tek satırla büyür.
func (s *Server) registerK8sCoverageRoutes(mux *http.ServeMux) {
	// Yalnız admin: kart bir TEŞHİS yüzeyi ve filo genelinde servis adı
	// listeliyor; viewer'ın operasyonel akışında yeri yok.
	mux.HandleFunc("GET /api/k8s/coverage", auth.RequireRole(auth.RoleAdmin, s.getK8sCoverage))
}

func (s *Server) getK8sCoverage(w http.ResponseWriter, r *http.Request) {
	sec, _ := strconv.ParseInt(r.URL.Query().Get("rangeS"), 10, 64)
	sec = snapK8sCoverageRange(sec)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	// Anahtar TÜM girdileri taşıyor (CLAUDE.md sert kısıtı). Pencere
	// basamaklı olduğu için kardinalite sınırlı ve cache gerçekten tutar.
	key := fmt.Sprintf("k8s-coverage:v1:r=%d:l=%d", sec, limit)
	s.serveCached(w, r, key, 60*time.Second, func(ctx context.Context) (any, error) {
		to := time.Now()
		from := to.Add(-time.Duration(sec) * time.Second)
		return s.store.GetK8sCoverage(ctx, from, to, limit)
	})
}
