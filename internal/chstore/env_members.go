package chstore

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

// v0.8.387 — env-separation Phase 3: /problems consumes the global
// `?env=` picker.
//
// Problems rows live in the `problems` state table keyed by
// (rule, service) — they carry NO env dimension, and a problem the
// evaluator computed over all-env metrics cannot be attributed to one
// env post-hoc. So the env filter on /problems honestly means:
// "show problems whose SERVICE ran in the selected env" — resolved
// through the service→env map below, the exact env twin of
// GetServiceClusterMap / clusterMemberServices (v0.8.386).

// GetServiceEnvMap returns one entry per service with the distinct
// deployment environments (spans.deploy_env) it emitted from during
// the last `since` window. Backs the /problems + /inbox env filter
// (service-scoped semantics, see EnvMemberServices).
//
// Single batched query — N+1-free regardless of problem count.
// deploy_env is a typed LowCardinality column, so unlike the cluster
// map's res/attr derive this GROUP BY is a cheap dict pass even at
// billion-span scale. Capped at 50000 rows (1000 services × 50 envs
// class of bound — far above any realistic install).
//
// Cached 60s per `since` (the v0.8.359 P2-C discipline, mirrored from
// GetServiceClusterMap): env membership is deploy-stable, so a minute
// of staleness is invisible, and the /problems + sidebar 30s polls
// never pay more than one map refresh per minute. The cached map is
// returned SHARED: callers must treat it as read-only.
func (s *Store) GetServiceEnvMap(ctx context.Context, since time.Duration) (map[string][]string, error) {
	if since == 0 {
		since = 1 * time.Hour
	}
	s.envMapMu.RLock()
	if s.envMapVal != nil && s.envMapFor == since &&
		time.Since(s.envMapAt) < envMapCacheTTL {
		v := s.envMapVal
		s.envMapMu.RUnlock()
		return v, nil
	}
	s.envMapMu.RUnlock()
	from := time.Now().Add(-since)
	rows, err := s.conn.Query(ctx, `
		SELECT service_name, deploy_env
		FROM spans
		WHERE time >= ? AND service_name != '' AND deploy_env != ''
		GROUP BY service_name, deploy_env
		ORDER BY service_name, deploy_env
		LIMIT 50000
		SETTINGS max_execution_time = 8`, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var svc, env string
		if err := rows.Scan(&svc, &env); err != nil {
			continue
		}
		out[svc] = append(out[svc], env)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	// Replace, never mutate — a reader holding the old snapshot stays
	// consistent (same discipline as the clusterMap / alertRules caches).
	s.envMapMu.Lock()
	s.envMapAt = time.Now()
	s.envMapFor = since
	s.envMapVal = out
	s.envMapMu.Unlock()
	return out, nil
}

// errEnvMapUnavailable — EnvMemberServices' cold conn-less path.
// Callers soft-fail to UNFILTERED on any error (never blank the
// triage page on a map blip); pinned in env_members_test.go.
var errEnvMapUnavailable = errors.New("service→env map unavailable")

// EnvMemberServices resolves an environment name to the sorted set
// of services that ran in it, from the 60s-cached 1h-clamped
// service→env map (v0.8.387 — the env twin of clusterMemberServices,
// v0.8.386). Exported because the /inbox handler applies the same
// service-scoped env semantics to its merged item list.
//
// Return contract (load-bearing, unlike clusterMemberServices' nil):
//   - (members, nil)  — authoritative; an EMPTY slice means the env
//     genuinely has no services in the last hour, and callers filter
//     to zero service-scoped rows (honest empty, not "show all").
//   - (nil, err)      — the map could not be resolved (cold conn-less
//     store, CH blip); callers MUST soft-fail to unfiltered so a
//     transient error never hides a firing P1.
func (s *Store) EnvMemberServices(ctx context.Context, env string) ([]string, error) {
	// Conn-less Stores (pure SQL-shape tests) may still carry a
	// SEEDED map cache; only a real cache miss needs the conn.
	s.envMapMu.RLock()
	fresh := s.envMapVal != nil && s.envMapFor == time.Hour &&
		time.Since(s.envMapAt) < envMapCacheTTL
	s.envMapMu.RUnlock()
	if !fresh && s.conn == nil {
		return nil, errEnvMapUnavailable
	}
	m, err := s.GetServiceEnvMap(ctx, time.Hour)
	if err != nil {
		return nil, err
	}
	out := []string{} // non-nil: empty is an authoritative "no members"
	for svc, envs := range m {
		for _, e := range envs {
			if e == env {
				out = append(out, svc)
				break
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// envScopeConjunct — env kapsamının SQL yazımı. TEK yer: /problems'ın
// WHERE'i (applyEnvServiceScope), rozet sayısı
// (CountProblemsNotInStatuses) ve şerit çipi (CountProblemsBySubject)
// aynı dizeyi üretir, yani üçü YAPISAL olarak aynı evreni sayar.
// Saf — yalnız üye SAYISI + iki-boot probe'u. Table-tested (v0.8.387,
// v0.9.1358).
//
// n = üye servis sayısı. Bağ argümanları çağıranın sorumluluğunda ve
// `service IN (?,?,…)` sırasına göre eklenir.
//
// Disjunkt kümesi (SIRAYLA — env_members_test.go'daki değerlendirici bu
// üç yazımı tanır, dördüncüsünü GÖRÜNCE testi düşürür):
//
//   - BOŞ service — global (log_query) satırlar HER ZAMAN geçer: bu
//     monitörler env'e atfedilemez ve operatör uat'a daralttı diye
//     ateş eden bir global alarmı gizlemek triage tehlikesidir.
//   - service IN (…) — üye servis geçer. Çok-env'li bir servis
//     koştuğu HER env'in üyesidir, yani satırı her birinde görünür.
//   - kind = 'db' — v0.9.1358: db ÖZNELİ satırlar geçer.
//
// NEDEN db kaçış kapısı (v0.9.1358, operatör-bildirimli sınıf).
// Bir db problem'inin `service` alanı bir DBSubjectID'dir
// (`db:oracle@corebank-scan.prod`, v0.9.1338) — ne boştur ne de bir env
// üyesi servis adı OLABİLİR. Kaçış kapısı olmadan `?env=` seçili her
// sayfada db problemlerinin TAMAMI sessizce düşerdi.
//
// Bu yeni bir ürün kararı DEĞİL, yukarıdaki ilk maddenin kendi
// gerekçesinin uygulanması: bir veritabanı tam olarak "env'e
// atfedilemeyen" özneye örnektir (aynı Oracle RAC'i int/uat/prod
// çağırır) ve dolan bir tablespace tam olarak "ateş eden alarm"dır.
// Eski yorumun bilinçli olarak gizlediği hâl — HARİTADA OLMAYAN bir
// SERVİS — aynen gizli kalıyor; ayırt edici TİPLİ ÖZNE (kind), haritada
// yokluk değil. Aynı taksit takım süzgecinde v0.9.1345'te ödendi
// (problemServicesConjunct/ServicesAllowDBSubjects); ikinci ve FARKLI
// şekilli bir kaçış kapısı yazmak bu deponun tekrar tekrar ödediği
// ayrışma olurdu.
//
// hasKindCol İKİ-BOOT sözleşmesinin girdisi (v0.9.1338), problemSubject
// Conjunct ile AYNI mantık: kolonu EKLEYEN boot'ta `kind` YOKTUR ve o
// boot'ta db özneli SATIR da yoktur (db_capacity.go kolon yokken kind'ı
// hiç yazmaz), dolayısıyla istisnayı hiç yazmamak DOĞRU cevaptır — var
// olmayan bir kolona sorgu göndermek değil.
//
// n == 0 (env HİÇBİR servise çözüldü) — cevap "global + db". Kümeyi
// yalnız global satırlara indirmek db satırlarını da öldürürdü; bir
// ortamın son 1 saatte hiç span'i olmaması, o kurulumdaki veritabanı
// alarmlarının yok sayılması demek değil.
func envScopeConjunct(n int, hasKindCol bool) string {
	parts := make([]string, 0, 3)
	parts = append(parts, "service = ''")
	if n > 0 {
		parts = append(parts, "service IN ("+chPlaceholders(n)+")")
	}
	if hasKindCol {
		// Literal GÜVENLİ: ProblemKindDB bir paket sabiti, kullanıcı
		// girdisi değil. Parametre bağlamak, çağıranın üye
		// argümanlarıyla sıra bağımlılığı yaratırdı (v0.9.1345 kararı).
		parts = append(parts, "kind = '"+ProblemKindDB+"'")
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// EnvScopeKeepsRow — env kapsamının SATIR düzeyindeki TEK tanımı:
// envScopeConjunct'ın SQL'de yaptığını Go'da yapar, /inbox'ın
// birleştirilmiş listesi için (o liste üç kaynaktan geliyor ve SQL'de
// süzülemiyor).
//
// v0.9.1358 — daha önce bu kural internal/api'de `envKeepsRow` olarak
// İKİNCİ kez yazılıydı ve iki yorum da "ayrışamazlar" diyordu; ayrışmayı
// engelleyen tek şey o düzyazıydı ve v0.9.1338 db özneleri geldiğinde
// İKİSİ BİRDEN yanlışa düştü. Artık tek gövde var: api tarafı bunu
// ÇAĞIRIYOR, kopyalamıyor.
//
// kind — satırın ÖZNE türü (Problem.Kind / InboxItem.SubjectKind), satırın
// KAYNAĞI değil. Boş değer ProblemSubjectKind ile "service"e normalize
// olur; bu, iki-boot sözleşmesinin Go ayağını bedavaya karşılar: kolonun
// olmadığı boot'ta her satır kind="" okur, yani hiçbiri db kaçış
// kapısından geçmez — SQL tarafının hasKindCol=false dalıyla aynı cevap.
func EnvScopeKeepsRow(service, kind string, members map[string]bool) bool {
	if service == "" {
		return true
	}
	if ProblemSubjectKind(kind) == ProblemKindDB {
		return true
	}
	return members[service]
}

// applyEnvServiceScope adds the env conjunct for the problems state
// table to wc. Pure — table-tested (v0.8.387, v0.9.1358).
// Semantics live on envScopeConjunct.
func applyEnvServiceScope(wc *whereClause, members []string, hasKindCol bool) {
	wc.add(envScopeConjunct(len(members), hasKindCol), toAnySlice(members)...)
}

// problemCountWhere — SAYIM sorgularının (sidebar rozeti
// CountProblemsNotInStatuses + şerit çipi CountProblemsBySubject) TAM
// WHERE gövdesi ve bağ argümanları.
//
// v0.9.1358 — bu ikisi whereClause kullanmıyor, dizeyi elle kuruyordu
// ve env yazımı ÜÇÜNCÜ, DÖRDÜNCÜ kez tekrar ediyordu. Ortak gövde
// olmasaydı liste düzelip sayılar eski kalırdı: operatörün gördüğü ilk
// şey, açtığı sayfayla çelişen bir rozet olurdu.
//
// Store METODU olması bilinçli: probe (hasProblemKindCol) TEK yerde
// okunuyor, yani çağıranın onu yanlış geçirmesi diye bir hâl YOK. Mutasyon
// turu bunu ölçtü — probe parametreyken iki çağrı da `false` yazılabiliyor
// ve hiçbir test ısırmıyordu.
//
// envServices: nil = kısıt yok. Non-nil = kısıt, ve BOŞ dilim ("env
// hiçbir servise çözüldü") YİNE kısıttır — nil/boş ayrımı taşıyıcı
// (v0.9.219). Argüman SIRASI da sözleşme: önce statü dışlamaları, sonra
// env üyeleri; ikisi de `?` ile bağlanıyor. Table-tested.
func (s *Store) problemCountWhere(exclude, envServices []string) (string, []any) {
	args := toAnySlice(exclude)
	sql := "1"
	if len(exclude) > 0 {
		sql = "status NOT IN (" + chPlaceholders(len(exclude)) + ")"
	}
	if envServices != nil {
		sql += " AND " + envScopeConjunct(len(envServices), s.hasProblemKindCol)
		args = append(args, toAnySlice(envServices)...)
	}
	return sql, args
}

// envScopeProblems resolves ProblemFilter.Env and applies the
// service-scoped conjunct. Shared by ListProblems AND CountProblems
// so the /problems list, the sidebar badge, and the buckets endpoint
// agree by construction. Soft-fails to unfiltered on a map error —
// list and count then agree on the UNfiltered numbers too.
func (s *Store) envScopeProblems(ctx context.Context, wc *whereClause, env string) {
	if env == "" {
		return
	}
	members, err := s.EnvMemberServices(ctx, env)
	if err != nil {
		return
	}
	applyEnvServiceScope(wc, members, s.hasProblemKindCol)
}
