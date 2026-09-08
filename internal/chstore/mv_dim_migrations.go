package chstore

// mv_dim_migrations.go — v0.10.563.
//
// Bu defter store.go'dan AYRI dosyada duruyor ve bu bilinçli: store.go'yu
// metin olarak tarayan testler var (topo_cluster_test.go `AS cluster`
// zincirini MV ADINDAN İLERİ arıyor). MV adını store.go içinde DDL'den
// ÖNCE geçen bir kod satırı o aramayı yanlış yere kaydırırdı — kapı
// sessizce başka bir MV'nin zincirini ölçerdi.

// mvDimMigration — sonradan BOYUT kazanmış bir MV'nin boot geçişi.
// Probe system.columns'ta Column'u arar: YOKSA MV drop + recreate edilir,
// VARSA hiç dokunulmaz (no-op — yerinde ALTER'la geçmiş kurulum ikinci kez
// düşmez). Dim yalnız log satırının okunabilir boyut etiketi.
type mvDimMigration struct {
	Table  string
	Column string
	Dim    string
}

// mvDimMigrations — geçiş defteri (SAF; mv_dim_migration_test.go pinler).
// Yeni bir boyut eklendiğinde DDL ile BİRLİKTE buraya satır düşer; yoksa
// mevcut kurulumlar eski şemayla kalır ve okuma yüzeyi sessizce boş döner.
var mvDimMigrations = []mvDimMigration{
	// v0.5.327 — db_summary_5m + db_caller_summary_5m db_name kazandı:
	// bir host'ta birden çok veritabanı ayrı satır olarak yüzsün.
	{Table: "db_summary_5m", Column: "db_name", Dim: "db_name"},
	{Table: "db_caller_summary_5m", Column: "db_name", Dim: "db_name"},
	// v0.10.563 — messaging_summary_5m operation kazandı (Messaging Faz 4b):
	// topic başına operasyon düzeyi RED. messaging_caller_summary_5m
	// DEĞİŞMEDİ — orada kırılım kind + service_name.
	{Table: "messaging_summary_5m", Column: "operation", Dim: "operation"},
}

// mvDimNeedsMigration — geçiş KARARI (SAF çekirdek). hasColumn,
// system.columns probe'unun sonucudur:
//
//   - kolon VAR  → false: hiç dokunma. Bu sözleşmenin kendisi bir veri
//     koruması — yerinde ALTER + MODIFY QUERY ile geçmiş bir kurulum
//     boot'ta 90 günlük messaging kovalarını KAYBETMEZ.
//   - kolon YOK  → true: drop + recreate (kovalar düşer, MV ileriye dolar).
func mvDimNeedsMigration(hasColumn bool) bool { return !hasColumn }
