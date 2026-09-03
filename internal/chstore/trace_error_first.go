package chstore

import (
	"context"
	"fmt"
	"time"
)

// trace_error_first.go — v0.10.307 (Operator-reported, prod v0.10.304:
// "6 saatte Errors trace'leri geliyor, 3 saat seçince 'No traces' — daha
// önce de olmuştu").
//
// Şekil: servis + Errors + attribute filtresi (name=POST) + süre sıralaması.
// Attribute filtresi MV yolunu kapatır (tracesMVEligible), ham yol çalışır:
// aşama 1 = spans üzerinde `WHERE service AND name='POST'` → GROUP BY
// trace_id HAVING max(error)=1 (v0.10.258: Errors, başka span yüklemi
// varken HAVING'dedir — "aynı span hem hata hem aranan" tuzağı). Yani
// `status_code='error'` budaması YOK: servisin penceredeki TÜM span'leri
// taranıp gruplanır. Prod'da bu 25 s bütçesini aşar → narrowOnExhaustion
// pencereyi ikiye böler (3 s → 1.5 s → 45 dk); eşleşen trace'ler 54-94 dk
// yaştaysa "boş" döner (dürüst ama gizli: `narrowedFromNs`). 6 s'nin
// gelmesi ısınmış cache'in şansı.
//
// Çare — HATA-ÖNCE aday daraltma: Errors istenen ve Errors HAVING'e düşmüş
// her sorguda önce servisin penceredeki HATALI span taşıyan trace id'leri
// okunur (PK service_name + idx_status set(0): hatalar nadir → ucuz,
// akışkan, zaman sıralı, tavan traceStage2MaxIDs), sonra aşama 1/2'nin
// WHERE'ine `trace_id IN (…)` (idx_trace bloom) eklenir. Anlam DEĞİŞMEZ:
// Errors zaten "trace'te servisin hatalı bir span'i var" demekti; filtre
// öteki span'lerde olabilir (HAVING kalır). Hiç hatalı trace yoksa cevap
// BOŞTUR ve doğrudur — tam tarama yapılmaz. Tavana çarpılırsa RankedWithin
// ("en yeni N hatalı trace içinde") — yalan değil, sınır ilanı.

const errorFirstOverfetch = 3

// errorFirstEligible — SAF: Errors HAVING yolunda + servis kapsamında.
func errorFirstEligible(f TraceFilter) bool {
	return f.HasError && !hasErrorSpanLocal(f) && f.Service != "" &&
		f.TraceID == "" && len(f.TraceIDs) == 0 && len(f.CandidateIDs) == 0
}

// errorFirstSQL — servisin hatalı span'leri, en yeniden eskiye; tekilleme
// Go'da (scanIDSlice deseni: akışkan, GROUP BY'sız).
func errorFirstSQL(whereSQL string) string {
	return `
		SELECT trace_id
		FROM spans ` + whereSQL + `
		ORDER BY time DESC
		LIMIT ?
		SETTINGS max_execution_time = 10,
		         distributed_product_mode = 'global'`
}

// errorFirstFilter — aday sorgusunun filtresi: pencere + servis + env/cluster
// KALIR; span-düzeyi öteki yüklemler (filtre/arama/kök/süre) DÜŞER — onlar
// aşama 1/2'de tam listeyle koşar. HasError kapatılıp `status_code =
// 'error'` açıkça eklenir (WHERE; idx_status budar).
func errorFirstFilter(f TraceFilter) TraceFilter {
	lf := f
	lf.HasError = false
	lf.Filters, lf.FilterRoot, lf.Search = nil, nil, ""
	lf.RequireServices, lf.RootOnly = nil, false
	lf.MinMs, lf.MaxMs = 0, 0
	lf.TraceIDs, lf.CandidateIDs = nil, nil
	return lf
}

// errorFirstCandidates — (idler, tavana çarptı mı, hata).
func (s *Store) errorFirstCandidates(ctx context.Context, f TraceFilter) ([]string, bool, error) {
	wc := buildGetTracesWhere(errorFirstFilter(f), s.clusterExpr())
	wc.add("status_code = 'error'")
	budget := traceStage2MaxIDs * errorFirstOverfetch
	rows, err := s.telemetryReadConn().Query(ctx, errorFirstSQL(wc.sql()), append(append([]any{}, wc.args...), budget)...)
	if err != nil {
		return nil, false, fmt.Errorf("error-first candidates: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]struct{}, 256)
	ids := make([]string, 0, 256)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false, err
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) >= traceStage2MaxIDs {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return ids, len(ids) >= traceStage2MaxIDs, nil
}

var _ = time.Second
