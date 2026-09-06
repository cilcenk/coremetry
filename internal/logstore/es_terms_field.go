package logstore

// es_terms_field.go — v0.10.500 (log arama denetimi A2 🔴): tek-alanlı
// terms agg'ların (servis kırılımı, desen başına servis) alan YAZIMI.
//
// Bulgu: süzgeç tarafı iki mapping şeklini de biliyor (exactTermsBothShapes:
// `f.keyword` term VEYA `f` term + .keyword yok), agg tarafı ise `.keyword`e
// çakılıydı. Yönetilen/ECS mapping'de (OpenShift cluster-logging, OTel ECS
// kipi) alan DÜZ keyword'dür ve `.keyword` alt-alanı yoktur → terms agg
// "unmapped" boş döner: servis kırılımı boş, desen kartı servissiz.
//
// Çözüm iki kademe, ikisi de cache'li (groupFieldTTL, pozitif + negatif):
//  1. field_caps (es_group_fields.go'daki resolveGroupAggFields kuralı:
//     çıplak keyword+aggregatable → çıplak; yoksa `.keyword` keyword ise o).
//  2. field_caps YOKSA (prod apikey'i yalnız doküman okur; _mapping /
//     field_caps 403 — project-prod-distributed-es) → probe: son 1 saatte
//     size:1 terms agg önce `.keyword` (kova varsa o), boşsa çıplak alan
//     (400 = text, `.keyword`e dön; kova varsa çıplak). En kötü iki küçük
//     istek / alan / 10 dk; asla istek başına.
//
// Sonuç bilinemezse bugünkü davranış (`.keyword`) korunur — boş kırılım,
// hiç 400 değil.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/elastic/go-elasticsearch/v8/esapi"
)

// termsProbeWindow — probe'un baktığı pencere: mapping sorusu için taze
// bir dilim yeter; geniş pencere maliyeti büyütür, cevabı değiştirmez.
const termsProbeWindow = time.Hour

// termsAggSpellingFromCaps — SAF: field_caps'ten terms agg yazımı; ""
// = bu alanda keyword yolu yok (yalnız text) — çağıran `.keyword`e düşer
// (unmapped → boş, 400 değil).
func termsAggSpellingFromCaps(bare string, caps map[string]traceFieldCap) string {
	r := resolveGroupAggFields([]string{bare}, caps)
	if len(r) == 0 {
		return ""
	}
	return r[0]
}

// termsAggSpellingFromProbe — SAF: probe sonuçlarından yazım.
// keywordBuckets > 0 → `.keyword` (bugünkü şekil doğrulandı);
// yoksa çıplak alan hatasız ve kovalıysa → çıplak (yönetilen mapping);
// ikisi de boş/hatalı → `.keyword` (bilinmiyor; bugünkü davranış).
func termsAggSpellingFromProbe(bare string, keywordBuckets int, bareErr error, bareBuckets int) string {
	if keywordBuckets > 0 {
		return bare + ".keyword"
	}
	if bareErr == nil && bareBuckets > 0 {
		return bare
	}
	return bare + ".keyword"
}

// termsProbeBody — SAF: size:1 terms agg, son termsProbeWindow, tüm
// v0.8.3 maliyet korumaları (size:0, track_total_hits:false, timeout).
func termsProbeBody(tsField, aggField string) map[string]any {
	return map[string]any{
		"size": 0,
		"query": map[string]any{"range": map[string]any{
			tsField: map[string]any{"gte": "now-" + fmt.Sprint(int(termsProbeWindow.Minutes())) + "m"},
		}},
		"aggs":             map[string]any{"p": map[string]any{"terms": map[string]any{"field": aggField, "size": 1}}},
		"track_total_hits": false,
		"timeout":          "3s",
	}
}

// termsProbeBuckets — probe'u çalıştırır; kova sayısı (0 = boş/unmapped).
// Hata = ES hatası (text alanda 400 dahil) — çağıran yorumlar.
func (s *ESStore) termsProbeBuckets(ctx context.Context, idx []string, aggField string) (int, error) {
	body, err := json.Marshal(termsProbeBody(s.fields.Timestamp, aggField))
	if err != nil {
		return 0, err
	}
	tru := true
	req := esapi.SearchRequest{
		Index: idx, Body: bytes.NewReader(body),
		AllowNoIndices: &tru, IgnoreUnavailable: &tru, RequestCache: &tru,
	}
	res, err := req.Do(ctx, s.cli)
	if err != nil {
		return 0, s.recordQueryError("terms-field probe", idx, body, 0, fmt.Errorf("ES terms probe: %w", err))
	}
	defer res.Body.Close()
	if res.IsError() {
		return 0, s.recordQueryError("terms-field probe", idx, body, res.StatusCode,
			parseESError("terms-field probe", res, s.cfg.Index))
	}
	var decoded struct {
		esSearchEnvelope // v0.10.413 (A5) — kısmi cevap görünür
		Aggregations     struct {
			P struct {
				Buckets []struct {
					DocCount int64 `json:"doc_count"`
				} `json:"buckets"`
			} `json:"p"`
		} `json:"aggregations"`
	}
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		return 0, err
	}
	if d := decoded.describe(); d != "" {
		// Kısmi cevapta "0 kova" mapping kanıtı değildir: hata olarak dön,
		// karar bugünkü `.keyword`e düşsün (bir sonraki TTL'de yeniden denenir).
		return 0, fmt.Errorf("terms probe partial (%s)", d)
	}
	return len(decoded.Aggregations.P.Buckets), nil
}

// termsAggField — `bare` için terms agg yazımı (cache'li). Boş bare → "".
func (s *ESStore) termsAggField(ctx context.Context, bare string) string {
	if bare == "" {
		return ""
	}
	key := "terms:" + bare
	now := time.Now()
	s.groupFields.mu.Lock()
	if v, ok := s.groupFields.byAxis[key]; ok && now.Before(v.expires) && len(v.fields) == 1 {
		f := v.fields[0]
		s.groupFields.mu.Unlock()
		return f
	}
	s.groupFields.mu.Unlock()

	pctx, cancel := context.WithTimeout(ctx, groupFieldCapsTimeout)
	defer cancel()
	idx := s.queryIndices(pctx, Filter{From: now.Add(-24 * time.Hour), To: now})
	spelling := ""
	if caps, err := s.fieldCaps(pctx, idx, envFieldCapsFields([]string{bare})); err == nil {
		spelling = termsAggSpellingFromCaps(bare, caps)
		if spelling == "" {
			spelling = bare + ".keyword" // yalnız text: unmapped `.keyword` boş döner, 400 değil
		}
		log.Printf("[logstore-es] terms agg field %q → %q (field_caps)", bare, spelling)
	} else {
		// field_caps yok (yetki/ağ): probe. Hata zaten /admin/elastic'e kaydedildi.
		pidx := s.queryIndices(pctx, Filter{From: now.Add(-termsProbeWindow), To: now})
		kw, kerr := s.termsProbeBuckets(pctx, pidx, bare+".keyword")
		bareBuckets, berr := 0, error(nil)
		if kerr != nil || kw == 0 {
			bareBuckets, berr = s.termsProbeBuckets(pctx, pidx, bare)
		}
		spelling = termsAggSpellingFromProbe(bare, kw, berr, bareBuckets)
		log.Printf("[logstore-es] terms agg field %q → %q (probe: keyword=%d/%v bare=%d/%v; field_caps: %v)",
			bare, spelling, kw, kerr, bareBuckets, berr, err)
	}
	s.groupFields.mu.Lock()
	if s.groupFields.byAxis == nil {
		s.groupFields.byAxis = map[string]esGroupFieldsVerdict{}
	}
	s.groupFields.byAxis[key] = esGroupFieldsVerdict{fields: []string{spelling}, expires: now.Add(groupFieldTTL)}
	s.groupFields.mu.Unlock()
	return spelling
}

// leanSourceFields — v0.10.500 (A4): Filter.LeanSource için `_source`
// includes: yalnız gövde + zaman + severity (mapHit'in bu üçü için
// okuduğu her yol). Desen örneklemesi 2000 dokümanı tam _source ile
// çekiyordu; okunan yalnız bunlardı.
func (s *ESStore) leanSourceFields() []string {
	cands := []string{s.fields.Body, s.fields.Timestamp, s.fields.SeverityTx, s.fields.SeverityNo,
		"level", "log.level", "severity_text", "severity"}
	seen := make(map[string]struct{}, len(cands))
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		if c == "" {
			continue
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}
