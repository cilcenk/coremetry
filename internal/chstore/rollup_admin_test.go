// rollup_admin_test.go — v0.9.770 rollup kurulum sihirbazının saf
// çekirdekleri. Canlı CH gerekmez; gömülü DDL dosyalarının GERÇEK
// içeriği üzerinden koşar (sentetik örnek, dosyalar değişince sessizce
// yalan söylerdi).
package chstore

import (
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/migrations"
)

func TestSplitSQLStatements(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "boş girdi",
			src:  "",
			want: nil,
		},
		{
			name: "yalnız yorum",
			src:  "-- sadece açıklama\n-- ikinci satır\n",
			want: nil,
		},
		{
			// Asıl tuzak: 0001/0003 başlıklarında `;` İÇEREN yorum
			// satırları var. Yorumlar önce atılmazsa buradaki `;`
			// sahte bir ifade sınırı üretir.
			name: "yorum içindeki noktalı virgül sınır SAYILMAZ",
			src:  "-- örnek: SELECT 1; SELECT 2;\nCREATE TABLE a (x Int64);\n",
			want: []string{"CREATE TABLE a (x Int64)"},
		},
		{
			name: "satır sonu yorumu kırpılır",
			src:  "CREATE TABLE a (x Int64); -- kuyruk yorumu\nCREATE TABLE b (y Int64);",
			want: []string{"CREATE TABLE a (x Int64)", "CREATE TABLE b (y Int64)"},
		},
		{
			name: "sondaki noktalı virgül boş ifade üretmez",
			src:  "CREATE TABLE a (x Int64);\n\n\n;\n",
			want: []string{"CREATE TABLE a (x Int64)"},
		},
		{
			name: "noktalı virgülsüz tek ifade",
			src:  "CREATE TABLE a (x Int64)",
			want: []string{"CREATE TABLE a (x Int64)"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitSQLStatements(tc.src)
			if len(got) != len(tc.want) {
				t.Fatalf("ifade sayısı = %d, beklenen %d (%q)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("ifade[%d] = %q, beklenen %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestSplitSQLStatementsEmbedded — GERÇEK gömülü dosyalar. Bölücü bu
// iki dosya için yazıldı; sentetik örnek geçip gerçeği bölemezse
// sihirbaz prod'da patlar.
func TestSplitSQLStatementsEmbedded(t *testing.T) {
	for _, f := range []string{"0001_rollup_narrow.sql", "0003_rollup_metrics.sql"} {
		t.Run(f, func(t *testing.T) {
			raw, err := migrations.FS.ReadFile(f)
			if err != nil {
				t.Fatalf("gömülü dosya okunamadı: %v", err)
			}
			stmts := SplitSQLStatements(string(raw))
			if len(stmts) == 0 {
				t.Fatal("hiç ifade çıkmadı")
			}
			for i, s := range stmts {
				// Her ifade CREATE ile başlamalı. Bir yorum artığı ya da
				// yanlış bölme burada yakalanır: "TTL ts + INTERVAL…"
				// gibi bir gövde parçası CREATE ile başlamaz.
				if !strings.HasPrefix(s, "CREATE ") {
					head := s
					if len(head) > 80 {
						head = head[:80]
					}
					t.Errorf("ifade[%d] CREATE ile başlamıyor: %q", i, head)
				}
				// Yorum artığı kalmamalı.
				if strings.Contains(s, "--") {
					t.Errorf("ifade[%d] yorum artığı içeriyor", i)
				}
			}
		})
	}
}

// TestEmbeddedFilesCarryClusterToken — Adapt'ın çevireceği token
// gerçekten dosyalarda mı. Dosya şablon adını değiştirirse (ör. başka
// bir küme adına elle güncellenirse) Adapt sessizce hiçbir şey
// değiştirmez ve DDL yanlış kümeye gider.
func TestEmbeddedFilesCarryClusterToken(t *testing.T) {
	for _, f := range []string{"0001_rollup_narrow.sql", "0003_rollup_metrics.sql"} {
		raw, err := migrations.FS.ReadFile(f)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", f, err)
		}
		if !strings.Contains(string(raw), rollupClusterToken) {
			t.Errorf("%s içinde %q token'ı yok — AdaptRollupDDL no-op olur", f, rollupClusterToken)
		}
	}
}

func TestAdaptRollupDDL(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		cluster string
		want    string
	}{
		{
			name:    "ON CLUSTER",
			src:     "CREATE TABLE x ON CLUSTER uptrace_all (a Int64)",
			cluster: "coremetry",
			want:    "CREATE TABLE x ON CLUSTER coremetry (a Int64)",
		},
		{
			// Distributed'ın İLK argümanı da küme adı. Yalnız
			// "ON CLUSTER" arayan bir regex bunu kaçırır ve tablo
			// var olmayan bir kümeye yönlenir.
			name:    "Distributed engine argümanı",
			src:     "ENGINE = Distributed(uptrace_all, currentDatabase(), x_local, rand())",
			cluster: "prod_cluster",
			want:    "ENGINE = Distributed(prod_cluster, currentDatabase(), x_local, rand())",
		},
		{
			name: "aynı metinde HER İKİ geçiş",
			src: "CREATE TABLE x ON CLUSTER uptrace_all AS y\n" +
				"ENGINE = Distributed(uptrace_all, currentDatabase(), y, rand());",
			cluster: "c1",
			want: "CREATE TABLE x ON CLUSTER c1 AS y\n" +
				"ENGINE = Distributed(c1, currentDatabase(), y, rand());",
		},
		{
			name:    "token yoksa değişmez",
			src:     "CREATE TABLE x (a Int64)",
			cluster: "c1",
			want:    "CREATE TABLE x (a Int64)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := AdaptRollupDDL(tc.src, tc.cluster); got != tc.want {
				t.Errorf("= %q\nbeklenen %q", got, tc.want)
			}
		})
	}
}

// TestAdaptRollupDDLEmbedded — gerçek dosyada TEK BİR token bile
// kalmamalı. Kalırsa o ifade "uptrace_all" adlı olmayan bir kümeye
// gider ve DDL kuyruğunda süresiz bekler (v0.9.613 sınıfı).
func TestAdaptRollupDDLEmbedded(t *testing.T) {
	for _, f := range []string{"0001_rollup_narrow.sql", "0003_rollup_metrics.sql"} {
		raw, err := migrations.FS.ReadFile(f)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", f, err)
		}
		out := AdaptRollupDDL(string(raw), "coremetry")
		if strings.Contains(out, rollupClusterToken) {
			t.Errorf("%s: dönüşümden sonra hâlâ %q geçiyor", f, rollupClusterToken)
		}
		if !strings.Contains(out, "ON CLUSTER coremetry") {
			t.Errorf("%s: ON CLUSTER coremetry üretilmedi", f)
		}
	}
}

// TestRollupRollbackStatements — 7 MV adının PİNİ. Bu liste bir MV
// eksik kalırsa geri alma yazımı KESMEZ; operatör "geri aldım" sanır
// ve ingest düşmeye devam eder.
func TestRollupRollbackStatements(t *testing.T) {
	tests := []struct {
		name    string
		cluster string
		target  string
		want    []string
	}{
		{
			name: "narrow", cluster: "c1", target: "narrow",
			want: []string{
				"DROP TABLE IF EXISTS mv_rollup_spans_narrow_10s ON CLUSTER c1 SYNC",
				"DROP TABLE IF EXISTS mv_rollup_spans_narrow_1m ON CLUSTER c1 SYNC",
				"DROP TABLE IF EXISTS mv_rollup_spans_narrow_5m ON CLUSTER c1 SYNC",
				"DROP TABLE IF EXISTS mv_rollup_spans_narrow_1h ON CLUSTER c1 SYNC",
			},
		},
		{
			name: "metrics", cluster: "c1", target: "metrics",
			want: []string{
				"DROP TABLE IF EXISTS mv_rollup_metrics_1m ON CLUSTER c1 SYNC",
				"DROP TABLE IF EXISTS mv_rollup_metrics_5m ON CLUSTER c1 SYNC",
				"DROP TABLE IF EXISTS mv_rollup_metrics_1h ON CLUSTER c1 SYNC",
			},
		},
		{
			name: "both = 7 MV", cluster: "coremetry", target: "both",
			want: []string{
				"DROP TABLE IF EXISTS mv_rollup_spans_narrow_10s ON CLUSTER coremetry SYNC",
				"DROP TABLE IF EXISTS mv_rollup_spans_narrow_1m ON CLUSTER coremetry SYNC",
				"DROP TABLE IF EXISTS mv_rollup_spans_narrow_5m ON CLUSTER coremetry SYNC",
				"DROP TABLE IF EXISTS mv_rollup_spans_narrow_1h ON CLUSTER coremetry SYNC",
				"DROP TABLE IF EXISTS mv_rollup_metrics_1m ON CLUSTER coremetry SYNC",
				"DROP TABLE IF EXISTS mv_rollup_metrics_5m ON CLUSTER coremetry SYNC",
				"DROP TABLE IF EXISTS mv_rollup_metrics_1h ON CLUSTER coremetry SYNC",
			},
		},
		{
			// Boş cluster → ON CLUSTER'sız. Çift boşluk ya da
			// "ON CLUSTER  SYNC" üretmek geçersiz SQL olurdu.
			name: "boş cluster → ON CLUSTER yok", cluster: "", target: "metrics",
			want: []string{
				"DROP TABLE IF EXISTS mv_rollup_metrics_1m SYNC",
				"DROP TABLE IF EXISTS mv_rollup_metrics_5m SYNC",
				"DROP TABLE IF EXISTS mv_rollup_metrics_1h SYNC",
			},
		},
		{
			name: "bilinmeyen target → both", cluster: "c1", target: "hepsi",
			want: rollupRollbackStatements("c1", "both"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := rollupRollbackStatements(tc.cluster, tc.target)
			if len(got) != len(tc.want) {
				t.Fatalf("ifade sayısı = %d, beklenen %d: %q", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("ifade[%d] = %q\nbeklenen %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestRollupRollbackCoversEveryMV — geri alma listesi, DDL'in YARATTIĞI
// her MV'yi kapsamalı. Kaynak dosyaya yeni bir kaskad eklenip listeye
// eklenmezse burada patlar.
func TestRollupRollbackCoversEveryMV(t *testing.T) {
	dropped := map[string]bool{}
	for _, stmt := range rollupRollbackStatements("c1", "both") {
		for _, f := range strings.Fields(stmt) {
			if strings.HasPrefix(f, "mv_") {
				dropped[f] = true
			}
		}
	}
	for _, file := range []string{"0001_rollup_narrow.sql", "0003_rollup_metrics.sql"} {
		raw, err := migrations.FS.ReadFile(file)
		if err != nil {
			t.Fatalf("%s okunamadı: %v", file, err)
		}
		for _, stmt := range SplitSQLStatements(string(raw)) {
			if !strings.HasPrefix(stmt, "CREATE MATERIALIZED VIEW") {
				continue
			}
			var name string
			for _, f := range strings.Fields(stmt) {
				if strings.HasPrefix(f, "mv_") {
					name = f
					break
				}
			}
			if name == "" {
				t.Errorf("%s: MV adı çıkarılamadı", file)
				continue
			}
			if !dropped[name] {
				t.Errorf("%s: %s yaratılıyor ama geri alma listesinde YOK", file, name)
			}
		}
	}
}

func TestRollupSources(t *testing.T) {
	tests := []struct {
		target  string
		want    []string
		wantErr bool
	}{
		{"narrow", []string{"0001_rollup_narrow.sql"}, false},
		{"metrics", []string{"0003_rollup_metrics.sql"}, false},
		{"both", []string{"0001_rollup_narrow.sql", "0003_rollup_metrics.sql"}, false},
		{"", nil, true},
		{"wide", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			got, err := rollupSources(tc.target)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("hata bekleniyordu, %q döndü", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("beklenmeyen hata: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("= %q, beklenen %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %q, beklenen %q", i, got[i], tc.want[i])
				}
			}
			// Adı doğru ama dosya gömülü değilse Apply çalışma anında
			// patlar — burada yakala.
			for _, f := range got {
				if _, err := migrations.FS.ReadFile(f); err != nil {
					t.Errorf("%s gömülü değil: %v", f, err)
				}
			}
		})
	}
}

func TestStmtHead(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"kısa ifade olduğu gibi", "CREATE TABLE a (x Int64)", "CREATE TABLE a (x Int64)"},
		{"satır sonları tek boşluğa iner", "CREATE TABLE a\n(\n  x Int64\n)", "CREATE TABLE a ( x Int64 )"},
		{
			"90 karakterde kesilir",
			strings.Repeat("A", 200),
			strings.Repeat("A", 90),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := stmtHead(tc.in)
			if got != tc.want {
				t.Errorf("= %q, beklenen %q", got, tc.want)
			}
			if len(got) > 90 {
				t.Errorf("uzunluk %d > 90", len(got))
			}
		})
	}
}
