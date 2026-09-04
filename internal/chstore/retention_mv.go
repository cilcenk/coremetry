package chstore

// retention_mv.go — v0.10.263 (perf profili §7 madde 7, S2 ⭐ + S5):
// MV depolarında partition düşürme — ŞEMA DEĞİŞİKLİĞİ YOK.
//
// S2 ⭐ ölçümü: spanmetrics_1s satır TTL'i 6 saat ama partition günlük;
// TTL merge'leri eski part'lara uğramadığından 397 saat tutulmuş. S5:
// trace_summary_5m / trace_service_index_5m TTL 90 gün, ham spans 30 gün
// (retention.spans ayarlanabilir) → MV'ler 3× uzun yaşıyor, id'leri
// çözülemeyen trace'lere işaret ediyor (disk + tarama).
//
// Audit reçetesi saatlik PARTITION + ttl_only_drop_parts idi = combined MV
// DROP+RECREATE (prod'da rolling-deploy okuma-hatası penceresi, migration).
// Bunun yerine mevcut saatlik retention enforcer'ı (StartRetentionEnforcer,
// v0.5.320: DROP PARTITION ile CH'nin merge-tabanlı TTL'ini beklemez) MV
// depolarına genişletiliyor: trace MV'leri retention.spans + 1 gün,
// spanmetrics_1s 1 gün, spanmetrics_10s 2 gün (row TTL'iyle aynı). Günlük
// partition'da "1 gün" = dün ve öncesi düşer, bugünkü part kalır (≤48 s
// yerine 397 s). Bir gün ilerisi saatlik partition istenirse migration.
//
// Combined MV'nin deposu `.inner_id.<uuid>`; ad host'lar arasında AYRIŞMIŞ
// olabilir (v0.9.620 dersi) → mvInnerTablesCluster küme genelinde çözer,
// her uuid için ayrı ALTER, hataları tek satır log. execDDL BİLEREK
// kullanılmıyor: adaptDDL tablo adına göre yeniden yazar ve MV'yi verirdi
// (applyExemplarColTTL emsali).

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

type mvRetentionTarget struct {
	mv   string
	days int
}

// mvRetentionTargets — SAF; testli. spansDays ≤ 0 → trace MV'leri atlanır
// (retention kapalı/ayarsız), spanmetrics tabakaları sabit.
func mvRetentionTargets(spansDays int) []mvRetentionTarget {
	out := []mvRetentionTarget{
		{"spanmetrics_1s", 1},
		{"spanmetrics_10s", 2},
	}
	if spansDays > 0 {
		out = append(out,
			mvRetentionTarget{"trace_summary_5m", spansDays + 1},
			mvRetentionTarget{"trace_service_index_5m", spansDays + 1},
		)
	}
	return out
}

// mvDropPartitionSQL — SAF.
//
// v0.10.355 — Operator-reported (self-telemetri, coremetry-worker ERROR
// span'i): `ALTER TABLE .inner_id.<uuid> ON CLUSTER … DROP PARTITION
// 2026-08-16` → code 62 "Syntax error at position 13 (.)". İki sözleşme
// kırığı: (1) yorum "inner adı backtick'li gelir" diyordu, mvInnerTablesCluster
// ÇIPLAK ad döndürüyor (exemplar TTL yolu kendi backtick'ini basıyor) —
// isim sözleşme ilan etti, kimse zorlamadı; (2) partition DEĞERİ
// (`2026-08-16`) tırnaksız gömülüyordu — Date anahtarında geçersiz.
// Şimdi: ad burada backtick'lenir (zaten backtick'li gelirse çift olmaz),
// partition `partition_id` ile `DROP PARTITION ID '…'` — anahtar tipinden
// bağımsız. MV saklama temizliği v0.5.320'den beri bu şekilde hiç
// çalışmamıştı; MV'ler yalnız satır TTL'iyle (merge tabanlı, geç) küçülüyordu.
func mvDropPartitionSQL(inner, partitionID, onCluster string) string {
	name := "`" + strings.Trim(inner, "`") + "`"
	return "ALTER TABLE " + name + onCluster + " DROP PARTITION ID '" + strings.ReplaceAll(partitionID, "'", "") + "'"
}

// stripBackticks — system.parts.table backtick'siz saklar.
func stripBackticks(s string) string { return strings.Trim(s, "`") }

func (s *Store) enforceMVRetention(ctx context.Context, spansDays int) {
	for _, t := range mvRetentionTargets(spansDays) {
		name := s.mvStorageName(t.mv)
		inners := s.mvInnerTablesCluster(ctx, name)
		if len(inners) == 0 {
			continue // MV yok ya da TO-table — düşürülecek depo yok
		}
		cutoff := time.Now().AddDate(0, 0, -t.days)
		for _, inner := range inners {
			parts, err := s.mvOldPartitions(ctx, stripBackticks(inner), cutoff)
			if err != nil {
				log.Printf("[retention] %s (%s): parts: %v", t.mv, inner, err)
				continue
			}
			for _, p := range parts {
				stmt := mvDropPartitionSQL(inner, p, s.onCluster())
				if err := s.conn.Exec(ctx, stmt); err != nil {
					// Ayrışmış uuid'de sahibi olmayan host code 60 verir — beklenen.
					log.Printf("[retention] %s DROP PARTITION %s: %v", t.mv, p, err)
					continue
				}
				log.Printf("[retention] dropped %s partition=%s (>%dd, %s)", t.mv, p, t.days, inner)
			}
		}
	}
}

// mvOldPartitions — max_time'ı cutoff'un altında kalan partition ID'leri; küme
// kipinde tüm host'lar taranır (inner uuid yalnız sahibinde olabilir).
func (s *Store) mvOldPartitions(ctx context.Context, table string, cutoff time.Time) ([]string, error) {
	src := "system.parts"
	if s.clusterMode() {
		src = fmt.Sprintf("clusterAllReplicas('%s', system.parts)", s.cfg.ClusterName)
	}
	// v0.10.355 — partition_id (`20260816`), partition değeri (`2026-08-16`) değil:
	// DROP PARTITION ID '…' her anahtar tipinde geçerli.
	rows, err := s.conn.Query(ctx, `
		SELECT partition_id
		FROM `+src+`
		WHERE database = currentDatabase() AND table = ? AND active = 1
		GROUP BY partition_id
		HAVING toUnixTimestamp64Nano(toDateTime64(max(max_time), 9)) < ?
		ORDER BY partition_id ASC
		LIMIT 400
		SETTINGS max_execution_time = 5`, table, cutoff.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
