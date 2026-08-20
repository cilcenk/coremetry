package chstore

// spool_ops.go — Distributed spool RUNBOOK'unun eller kısmı (v0.9.1191,
// operatör isteği: "Runbook sen otomatik ekle yeni versiyonda ben
// çalıştırayım").
//
// Bağlam: 2026-08-20 prod olayı — metric_points spool'u 3,04M dosya /
// 965 GiB, drenaj ETA'sı 34+ gün. Teşhis katmanı zaten üründeydi
// (distribution_queue.go, v0.9.985); müdahale ise prod CH node'unda
// clickhouse-client istiyordu çünkü /admin/sql bilinçli salt-okunur.
// Bu dosya o iki müdahaleyi ürüne, admin-kapılı ve audit'li olarak alır:
//
//	SYSTEM START DISTRIBUTED SENDS <t>  — durdurulmuş göndericiyi açar
//	SYSTEM FLUSH DISTRIBUTED <t>        — kuyruğu SENKRON boşaltır
//
// Bellek tavanı / spool dosyası silme gibi adımlar BİLEREK dışarıda:
// biri pod limiti (k8s), diğeri veri kaybı — ikisi de uygulamanın kendi
// başına basabileceği düğmeler değil. Panel onları metin olarak anlatır.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// chIdentRe — SYSTEM komutuna girecek tablo adının katı biçimi.
//
// Backtick'le tırnaklıyoruz ama tırnaklamaya GÜVENMİYORUZ: ada backtick,
// boşluk ya da nokta sokan bir istek, membership kontrolünden önce burada
// ölür. Telemetri tabloları hep bu biçimde (spans, metric_points, …).
var chIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidSpoolTable — ad hem biçimsel olarak güvenli hem de bu kurulumda
// GERÇEK bir Distributed tablo mu? İki katman bilinçli: regex enjeksiyonu,
// membership ise "yanlış ama zararsız görünen" hedefi keser (örn. _local
// adına flush istemek — Distributed değildir, CH zaten reddederdi ama
// operatöre bizim cevabımız sürücü hatasından net olmalı).
func (s *Store) ValidSpoolTable(ctx context.Context, table string) (bool, error) {
	if !chIdentRe.MatchString(table) {
		return false, nil
	}
	names, err := s.ListDistributedTables(ctx)
	if err != nil {
		return false, err
	}
	for _, n := range names {
		if n == table {
			return true, nil
		}
	}
	return false, nil
}

// ListDistributedTables — bu veritabanındaki Distributed motorlu tablolar.
// Runbook'un düğme hedefleri ve doğrulama evreni.
func (s *Store) ListDistributedTables(ctx context.Context) ([]string, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT name FROM system.tables
		WHERE database = currentDatabase() AND engine = 'Distributed'
		ORDER BY name
		SETTINGS max_execution_time = 5`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// DiskFree — bir düğümdeki bir diskin doluluk durumu. Runbook'un 0.
// adımı: spool 965 GiB'ken önce disk taşar mı sorusu cevaplanır.
type DiskFree struct {
	Host  string `json:"host"`
	Disk  string `json:"disk"`
	Free  uint64 `json:"free"`
	Total uint64 `json:"total"`
}

// CollectDisks — disk durumu, kümede DÜĞÜM BAŞINA.
//
// clusterAllReplicas, cluster() değil: disk düğüm-yerel durumdur, veri
// değil — cluster() her shard'dan tek replika okur ve 2×2 kümede
// disklerin yarısını görünmez yapardı (v0.9.454'ün system.disks bulgusu;
// distribution_queue.go aynı gerekçeyle aynı seçimi yapar). Tek düğümde
// düz okuma. Fan-out düşerse yerel okumaya düşülür — kısmi, ama runbook
// anında körlükten iyi; Host alanı zaten hangi düğüm olduğunu söyler.
func (s *Store) CollectDisks(ctx context.Context) ([]DiskFree, error) {
	const sel = `SELECT hostName(), name, toUInt64(free_space), toUInt64(total_space)`
	if cn := strings.TrimSpace(s.cfg.ClusterName); cn != "" {
		q := fmt.Sprintf(sel+`
			FROM clusterAllReplicas('%s', system.disks)
			ORDER BY hostName(), name
			SETTINGS skip_unavailable_shards = 1, max_execution_time = 5`,
			strings.ReplaceAll(cn, "'", ""))
		if out, err := s.scanDisks(ctx, q); err == nil {
			return out, nil
		}
	}
	return s.scanDisks(ctx, sel+`
		FROM system.disks
		ORDER BY name
		SETTINGS max_execution_time = 5`)
}

func (s *Store) scanDisks(ctx context.Context, q string) ([]DiskFree, error) {
	rows, err := s.conn.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiskFree
	for rows.Next() {
		var d DiskFree
		if err := rows.Scan(&d.Host, &d.Disk, &d.Free, &d.Total); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// spoolFlushReadTimeout — FLUSH bağlantısının okuma zaman aşımı.
//
// Ana havuzun 30 sn'si (v0.8.340) burada KULLANILAMAZ: 3M dosyalık bir
// spool'un senkron flush'ı saatler sürer ve SYSTEM komutu süresince
// sürücüye progress paketi akmayabilir — 30 sn'de kopan bağlantı sunucu
// tarafındaki flush'ı da iptal eder ve düğme "çalışmıyor" diye okunurdu.
// 24 saat: gözlenen en derin spool için bile tavan; asıl iptal yolu
// çağıranın context'i (uygulama kapanışı dahil).
const spoolFlushReadTimeout = 24 * time.Hour

// FlushDistributed — SYSTEM FLUSH DISTRIBUTED, KENDİ tek bağlantısında.
//
// Çağıran (internal/api) adı ValidSpoolTable'dan geçirmiş olmalı; yine de
// regex burada da koşar (savunma katmanları bağımsız). Bağlantı işlem
// sonunda kapanır — saatlik bir işlem için havuzdan bağlantı rehin almak,
// havuzun 30 sn varsayımlarını da bozmak demekti.
func (s *Store) FlushDistributed(ctx context.Context, table string) error {
	if !chIdentRe.MatchString(table) {
		return fmt.Errorf("geçersiz tablo adı: %q", table)
	}
	if s.chOpts == nil {
		return fmt.Errorf("uzun-işlem bağlantısı bu kurulumda yok")
	}
	opts := s.chOpts()
	opts.ReadTimeout = spoolFlushReadTimeout
	opts.MaxOpenConns = 1
	opts.MaxIdleConns = 1
	conn, err := clickhouse.Open(opts)
	if err != nil {
		return fmt.Errorf("flush bağlantısı: %w", err)
	}
	defer conn.Close()
	return conn.Exec(ctx, "SYSTEM FLUSH DISTRIBUTED `"+table+"`")
}

// StartDistributedSends — durdurulmuş göndericiyi açar. Anlık bir komut;
// ana bağlantı yeter. Idempotent: zaten açıksa CH sessizce OK der.
func (s *Store) StartDistributedSends(ctx context.Context, table string) error {
	if !chIdentRe.MatchString(table) {
		return fmt.Errorf("geçersiz tablo adı: %q", table)
	}
	return s.conn.Exec(ctx, "SYSTEM START DISTRIBUTED SENDS `"+table+"`")
}
