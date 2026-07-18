package chstore

import (
	"context"
	"fmt"
	"strings"
)

// runtimeAttrCond — "bu span rozet verisi taşıyor" SQL koşulu
// (v0.9.45). Badge'in render şartıyla (formatRuntime: language /
// runtime name / SDK version'dan biri dolu) birebir aynı üçlü;
// batch (argMaxIf) ve tekil (WHERE) sorguları AYNI sabiti kullanır
// ki iki yol asla ayrışmasın — runtime_test.go pinler.
const runtimeAttrCond = `(has(res_keys, 'telemetry.sdk.language') OR has(res_keys, 'process.runtime.name') OR has(res_keys, 'telemetry.sdk.version'))`

// ServiceRuntime is the technology fingerprint of a service —
// what SDK / language / runtime version is emitting telemetry.
// Powers the small "Java OpenJDK 21" / "Go 1.22" / ".NET 8.0"
// badge above the infra panel on /service?name=… so the
// operator instantly knows what stack they're investigating
// before they pick a runtime-specific debugger.
//
// All fields are optional — many SDKs only set a subset. The
// frontend renders whatever is non-empty in priority order:
// language + runtime version > runtime name > SDK version.
type ServiceRuntime struct {
	Language        string `json:"language,omitempty"`        // telemetry.sdk.language: "go", "java", "dotnet", "nodejs", "python"
	SDKVersion      string `json:"sdkVersion,omitempty"`      // telemetry.sdk.version
	RuntimeName     string `json:"runtimeName,omitempty"`     // process.runtime.name: "OpenJDK Runtime Environment", "go", ".NET"
	RuntimeVersion  string `json:"runtimeVersion,omitempty"`  // process.runtime.version: "21.0.1+12", "go1.22.5", "8.0.4"
	RuntimeDesc     string `json:"runtimeDesc,omitempty"`     // process.runtime.description: full free-text
	Host            string `json:"host,omitempty"`            // host.name (last seen)
	OS              string `json:"os,omitempty"`              // os.type
	Service         string `json:"service"`                   // pass-through
}

// GetAllServiceRuntimes returns the technology fingerprint
// for every service that emitted spans in the last hour.
// Single CH query using argMaxIf(field, time, <has runtime
// attrs>) to extract the latest ATTRIBUTE-BEARING res_keys /
// res_values per service in one pass — avoids the N-services ×
// N-requests fan-out the /services listing page would
// otherwise hit.
//
// v0.9.45 (operator-reported: "çoğu serviste rozet yok"):
//  1. argMax → argMaxIf: bir servis birden fazla resource'tan
//     span basıyorsa (app + sidecar, karışık pipeline) düz
//     argMax en-son span'i seçiyordu — attribute'suz kaynak son
//     basmışsa rozet piyango gibi kayboluyordu. Koşul, badge'in
//     render şartıyla birebir aynı üçlü.
//  2. LIMIT 500 → ORDER BY count() DESC LIMIT 2000: sırasız
//     LIMIT, 500+ aktif servisli filoda RASTGELE bir alt kümeye
//     rozet verip gerisini kalıcı rozetsiz bırakıyordu (hangi
//     500'ün geldiği cache yenilendikçe değişiyordu). Artık en
//     aktif 2000 servis deterministik olarak kapsanır.
//
// Output is a map keyed by service name. Missing services
// (no span in the lookback, or beyond the top-2000) are absent
// from the map; callers render the badge only for the names
// that exist. A service whose spans carry NO runtime attrs gets
// empty fields → the badge stays hidden (honest, not lottery).
func (s *Store) GetAllServiceRuntimes(ctx context.Context) (map[string]ServiceRuntime, error) {
	// v0.9.46 — pencere 1h → 15m: sorgunun maliyeti res_keys/res_values
	// Array kolonlarını taramaktan gelir; prod hacminde (1B+ span/gün,
	// paylaşımlı yavaş CH) 1 saatlik tarama 10s bütçeyi aşıp endpoint'i
	// KOMPLE boş düşürüyordu — listede hiç rozet yokken tekil (PK-pruned)
	// detay sorgusu çalışıyordu, operatör raporu. 15 dakika aktif basan
	// her servisi yakalar; boşta kalanları API katmanındaki merge-cache
	// taşır (api/service_runtimes_cache.go).
	rows, err := s.conn.Query(ctx, fmt.Sprintf(`
		SELECT
		  service_name,
		  argMaxIf(res_keys,   time, %[1]s) AS keys,
		  argMaxIf(res_values, time, %[1]s) AS vals
		FROM spans
		WHERE time >= now() - INTERVAL 15 MINUTE
		  AND service_name != ''
		GROUP BY service_name
		ORDER BY count() DESC
		LIMIT 2000
		SETTINGS max_execution_time = 10`, runtimeAttrCond))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]ServiceRuntime, 64)
	for rows.Next() {
		var name string
		var keys, vals []string
		if err := rows.Scan(&name, &keys, &vals); err != nil {
			return nil, err
		}
		rt := ServiceRuntime{Service: name}
		pick := func(k string) string {
			for i := range keys {
				if i < len(vals) && keys[i] == k {
					return strings.TrimSpace(vals[i])
				}
			}
			return ""
		}
		rt.Language       = pick("telemetry.sdk.language")
		rt.SDKVersion     = pick("telemetry.sdk.version")
		rt.RuntimeName    = pick("process.runtime.name")
		rt.RuntimeVersion = pick("process.runtime.version")
		rt.RuntimeDesc    = pick("process.runtime.description")
		rt.Host           = pick("host.name")
		rt.OS             = pick("os.type")
		out[name] = rt
	}
	return out, rows.Err()
}

// GetServiceRuntime returns the technology fingerprint for one
// service. Reads the latest span's resource attributes — the
// OTel SDK stamps these on every span so we just need one
// recent row to know the runtime. Falls back gracefully when
// any individual key is missing.
//
// Implementation: one row from the spans table over the last
// hour, scanning all res_keys / res_values arrays in-process
// for the keys we want. The query is partition-pruned (time
// filter), service_name is the primary key prefix, so this is
// a microsecond CH lookup even at 1B spans/day.
func (s *Store) GetServiceRuntime(ctx context.Context, service string) (*ServiceRuntime, error) {
	if service == "" {
		return nil, nil
	}
	// v0.9.45 — attribute-taşıyan EN SON span (düz "son span"
	// değil): karışık-resource servislerde rozet piyangosunu
	// bitirir (GetAllServiceRuntimes'taki argMaxIf'in tekil eşi).
	row := s.conn.QueryRow(ctx, fmt.Sprintf(`
		SELECT res_keys, res_values
		FROM spans
		WHERE service_name = ?
		  AND time >= now() - INTERVAL 1 HOUR
		  AND %s
		ORDER BY time DESC
		LIMIT 1
		SETTINGS max_execution_time = 5`, runtimeAttrCond), service)
	var keys []string
	var values []string
	if err := row.Scan(&keys, &values); err != nil {
		// "no rows" or any other error → return an empty
		// runtime so the frontend still renders the page;
		// the badge component just doesn't show up.
		return &ServiceRuntime{Service: service}, nil
	}
	out := &ServiceRuntime{Service: service}
	pick := func(k string) string {
		for i := range keys {
			if i < len(values) && keys[i] == k {
				return strings.TrimSpace(values[i])
			}
		}
		return ""
	}
	out.Language       = pick("telemetry.sdk.language")
	out.SDKVersion     = pick("telemetry.sdk.version")
	out.RuntimeName    = pick("process.runtime.name")
	out.RuntimeVersion = pick("process.runtime.version")
	out.RuntimeDesc    = pick("process.runtime.description")
	out.Host           = pick("host.name")
	out.OS             = pick("os.type")
	return out, nil
}
