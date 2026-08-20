package chstore

// exception_storm.go — fırtına dedektörünün OKUMA yarısı (v0.9.1194).
//
// Soru tek ve dar: "son W dakikada İLK KEZ görülen exception grupları
// hangi servislerden geldi?" Üyelik yalnız YENİ gruplar (first_seen
// pencere içinde — spec onayı): pencere içinde aktifleşen kronik bir
// grup fırtına kanıtı değildir, onu sayan bir dedektör gürültülü
// filolarda sürekli öterdi.

import (
	"context"
	"time"
)

// ExceptionStormCandidate — pencerede yeni grup açmış bir servis.
type ExceptionStormCandidate struct {
	Service     string
	Groups      uint64 // pencerede AÇILAN grup sayısı
	Occurrences uint64 // o grupların toplam olayı
}

// RecentNewExceptionServices — first_seen ≥ since olan gruplar, servis
// başına toplanmış, en çok grup açan önce.
//
// state != 'ignored': operatörün açıkça susturduğu bir grup fırtına
// kanıtına giremez. resolved DAHİL (bilinçli): "yeni açıldı ve hızla
// kendiliğinden kapandı" da fırtınanın parçasıdır — olay yaşandı.
// FINAL: ReplacingMergeTree ev kuralı; LIMIT 50 — eşik 5 civarında
// yaşar, 50'den kalabalık bir fırtınada ilk 50 zaten hikâyeyi anlatır.
func (s *Store) RecentNewExceptionServices(ctx context.Context, since time.Time) ([]ExceptionStormCandidate, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT service,
		       toUInt64(count())          AS groups,
		       toUInt64(sum(occurrences)) AS occ
		FROM exception_groups FINAL
		WHERE first_seen >= toDateTime64(?, 9, 'UTC')
		  AND state != 'ignored'
		GROUP BY service
		ORDER BY groups DESC, service
		LIMIT 50
		SETTINGS max_execution_time = 5`, chDateTime64Arg(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExceptionStormCandidate
	for rows.Next() {
		var c ExceptionStormCandidate
		if err := rows.Scan(&c.Service, &c.Groups, &c.Occurrences); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
