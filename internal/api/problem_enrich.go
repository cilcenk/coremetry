// v0.9.553 — problem okuma zenginleştirmesi TEK çağrı noktası.
//
// Operatör raporu: "Bazen P1 alertleri karıştırıyor ya da ben Problems
// sekmesinde chatte yazanları göremiyorum, orada da conflict var."
//
// Kök sebep: "P1" bir ClickHouse kolonu değil, OKUMA ANI hesabı
// (chstore.computePriority). Hesabın kritik kolu RecentDeploy'a bakar:
//
//	postDeploy := p.RecentDeploy != nil && ... AgeSeconds <= 5*60
//	case postDeploy: return "P1", "critical + deploy Ns before"
//	default:         return "P2", "critical"
//
// Yani deploy zenginleştirmesi koşmamışsa RecentDeploy nil kalır ve
// AYNI SATIR P2 olur. Sayfa yolları ikisini sırayla çağırıyordu,
// sohbet yolları (guided bundle'ları + vardiya özeti) yalnız
// priority'yi çağırıyordu — sonuç: taze deploy sonrası kritik bir
// problem sayfada P1, sohbette P2. Operatör iki yüzeyi yan yana
// koyduğunda çelişki görüyordu ve İKİSİ DE kendi içinde tutarlıydı,
// bu yüzden hangisinin yanlış olduğu belli olmuyordu.
//
// Ayrışma yönü tek taraflıydı: sohbet ASLA sayfadan daha acil
// olamıyordu, hep daha sakin görünüyordu. Bu, en kötü yön — operatör
// sohbete güvenip acil bir şeyi ertelemiş olabilir.
//
// Değişmez kural api.go'da zaten YAZILIYDI ("the deploys enrich must
// run before the priority enrich") ama yorum olarak; yorum derlenmez
// ve beş çağrı noktası onu ihlal etti. Bu dosya kuralı ÇAĞRILABİLİR
// hale getiriyor: iki adım tek fonksiyonda, sırası içeride sabit.
package api

import (
	"context"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// problemDeployLookback — priority hesabının deploy penceresi.
// Sayfa yollarının (api.go, inbox.go) kullandığı değerle AYNI olmak
// zorunda: farklı olsaydı iki yüzey yine ayrışırdı, sadece daha ince
// bir şekilde.
const problemDeployLookback = 30 * time.Minute

// enrichProblemsForRead — bir problem sayfasını OKUMA için hazırlar:
// önce deploy, sonra öncelik. Sıra zorunlu (bkz. dosya başı).
//
// Her problem listesi gösteren yol bunu çağırmalı — sayfa, sohbet,
// vardiya özeti, bildirim. Tek tek EnrichProblemsWithPriority
// çağırmak, düzeltilen hatayı geri getirir.
//
// Maliyet: satır başına değil, SAYFA başına tek toplu CH sorgusu ve o
// da 15s TTL cache'li (chstore.fetchDeploysByService). Sohbet yolunda
// kabul edilebilir; zaten aynı pencere için sayfa yolu da sorduğundan
// çoğu zaman cache'ten döner.
func (s *Server) enrichProblemsForRead(ctx context.Context, probs []chstore.Problem) []chstore.Problem {
	if len(probs) == 0 {
		return probs
	}
	// Zincirin kendisi chstore'da (v0.9.554): ikinci tüketici MCP
	// list_problems aracı ve o api.Server'a erişemiyor. İki paketin
	// ayrı ayrı "deploy sonra öncelik" yazması, düzeltilen ayrışmanın
	// yeni bir kopyası olurdu.
	return s.store.EnrichProblemsForRead(ctx, probs, problemDeployLookback)
}
