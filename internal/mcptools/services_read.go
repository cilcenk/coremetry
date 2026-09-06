package mcptools

import (
	"context"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

// services_read.go — servis RED okumasının MV yolu (v0.10.25, Copilot
// denetimi bulgusu).
//
// ── KUSUR: AÇIKLAMA MODELE YALAN SÖYLÜYORDU ─────────────────────────────
//
// `list_services` açıklaması modele şunu diyordu:
//
//	"Reads the 5-minute pre-aggregate so it's cheap to call repeatedly"
//
// Handler ise KOŞULSUZ olarak `GetServicesFilteredIn` → `GetServicesQuery`
// → ham `spans` GROUP BY çağırıyordu. MV ikizi (`GetServicesAggFiltered2`)
// zaten VAR ve `/api/services` onu `servicesUseMV` kapısıyla kullanıyor;
// yalnız tool'da kapı yoktu.
//
// İki ayrı zarar:
//
//  1. CLAUDE.md sert kısıtı: MV varken ham `spans` okumak BUG. Bu iki
//     tool kataloğun en çok çağrılacakları ("şu an hangi servis
//     sağlıksız" giriş noktası) ve model onları 5 turluk döngüde birden
//     çok kez çağırabiliyor.
//  2. Açıklama, modelin MALİYET MODELİdir. "Ucuz, tekrar tekrar çağır"
//     diyen bir katalog, küçük modeli döngüde tekrar çağırmaya İTİYOR —
//     yani yalan, kendi maliyetini büyütüyor.
//
// ── NEDEN İKİ YOL DA GEREKLİ ────────────────────────────────────────────
//
// MV'de `deploy_env` boyutu YOK (aynı sebeple /api/services'in kapısı
// `env == ""` istiyor). Operatör env'e daralttığında okuma ham span'lere
// düşmek ZORUNDA. Kapı bu yüzden kaldırılamıyor; yapılabilecek şey onu
// tool'a da uygulamak ve açıklamayı iki yolu da anlatacak şekilde
// düzeltmek.
//
// Pencere eşiği de aynı gerekçeyle duruyor: MV kovaları 5 dakikalık, daha
// dar bir pencerede MV'den okumak kovanın tamamını o pencereye atfetmek
// olurdu.

// servicesReadUseMV — MV yolu uygun mu.
//
// `/api/services`'in `servicesUseMV` kapısıyla AYNI sözleşme; cluster
// boyutu tool yüzeyinde hiç yok, o yüzden iki girdi yeterli.
func servicesReadUseMV(window time.Duration, env string) bool {
	return window >= 5*time.Minute && env == ""
}

// readServices — servis RED listesi, uygun olduğunda MV'den.
//
// Dönüş tipi iki yolda da `[]chstore.ServiceSummary`; çağıran hangi yolun
// koştuğunu bilmek zorunda değil. İkinci dönüş, cevabın zarfında
// operatöre/modele ilan edilebilsin diye kaynağı söylüyor — bir maliyet
// iddiası ölçülmeden yazılmamalı, ve artık yazılmıyor.
func readServices(
	ctx context.Context, d Deps, from, to time.Time,
	nameContains, env string, limit int,
) ([]chstore.ServiceSummary, string, error) {
	if servicesReadUseMV(to.Sub(from), env) {
		rows, err := d.Store.GetServicesAggFiltered2(
			ctx, from, to, nameContains, nil, "rps", "desc", limit, 0,
			chstore.ServiceDisplayFilters{})
		if err == nil {
			return rows, "service_summary_5m", nil
		}
		// MV yolu düştü: ham yola geç ama SESSİZCE değil — kaynak
		// alanı "spans" diyecek, yani cevabın kendisi hangi yoldan
		// geldiğini taşıyor.
	}
	rows, err := d.Store.GetServicesFilteredIn(
		ctx, 0, from, to, nameContains, nil, "rps", "desc", limit, 0, "", env)
	if err != nil {
		return nil, "", err
	}
	return rows, "spans", nil
}

// readServicesIn — v0.10.464 (D3): ad KÜMESİ için readServices ikizi (bulanık
// çözümden gelen tam adlar). Aynı MV/ham yol kararı, aynı kaynak etiketi.
func readServicesIn(
	ctx context.Context, d Deps, from, to time.Time,
	names []string, env string, limit int,
) ([]chstore.ServiceSummary, string, error) {
	if len(names) == 0 {
		return nil, "", nil
	}
	if servicesReadUseMV(to.Sub(from), env) {
		rows, err := d.Store.GetServicesAggFilteredIn(ctx, from, to, "", names, "rps", "desc", limit, 0)
		if err == nil {
			return rows, "service_summary_5m", nil
		}
	}
	rows, err := d.Store.GetServicesFilteredIn(ctx, 0, from, to, "", names, "rps", "desc", limit, 0, "", env)
	if err != nil {
		return nil, "", err
	}
	return rows, "spans", nil
}

// serviceCatalogueMax — bulanık çözüm için okunan katalog tavanı
// (api.guidedServiceNames ile aynı: 2000).
const serviceCatalogueMax = 2000

// resolveServiceFuzzy — v0.10.464 (D3): ifadeyi canlı katalogla çözer
// (ResolveServiceAmong). Katalog okuması picker'ın DISTINCT'i (ucuz).
func resolveServiceFuzzy(ctx context.Context, d Deps, phrase string) (exact string, candidates []string, err error) {
	names, _, err := d.Store.ListServiceNames(ctx, "", serviceCatalogueMax, 0)
	if err != nil {
		return "", nil, err
	}
	exact, candidates = ResolveServiceAmong(phrase, names, 8)
	return exact, candidates, nil
}
