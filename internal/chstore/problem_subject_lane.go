package chstore

import "context"

// v0.9.1342 — /inbox'ın özne şeridi (operatör kararı: db problemleri AYRI
// şeritte, servis problemleriyle öncelik sırasında YARIŞMASIN).
//
// Şeridin tamamı bu iki parçaya dayanıyor:
//   - problemSubjectConjunct — WHERE cümlesi, SAF fonksiyon
//   - CountProblemsBySubject — çip sayıları, tek COUNT
//
// ═══ NEDEN GO'DA YENİDEN NORMALİZE ETMİYORUZ ═══
//
// `problems.kind` (v0.9.1338) `LowCardinality(String) DEFAULT 'service'`.
// Go okuma yolu ayrıca ProblemSubjectKind() ile boş değeri 'service'e
// çeviriyor. O normalizasyon SATIR OKUNDUKTAN sonra çalışır — şerit ise
// bir SQL sorgusudur ve LIMIT'ten ÖNCE ısırmalıdır (v0.9.322 dersi:
// LIMIT, gösterilecek satırlara uygulanmalı).
//
// Yani şerit doğrudan CH'nin DEFAULT'una GÜVENİR. Bu güven ölçülmüş ve
// pinlenmiştir: TestProblemKindColumnDefaultsToService (v0.9.1338)
// store.go'daki ALTER metnini sabitler. O default `''` olsaydı 4800+
// geçmiş satır sessizce YANLIŞ şeride düşerdi — ve bunu ancak SQL'e
// bakan bir yüzey görürdü, Go okuma yolu farkı yutardı.
//
// ⚠ Bu yüzden burada "boş dize VEYA service" biçiminde bir OR YAZILMAZ.
// Öyle bir ifade default'un bozulmasını GİZLER: kapı yeşil kalır,
// garanti çürür. TestSubjectLaneDoesNotHideTheColumnDefault o ifadenin
// dosyaya girmediğini tarar — bu yorumun da o kalıbı harfiyen
// içermemesinin sebebi bu (kendi kapısını tetikleyen açıklama sınıfı).

// problemSubjectConjunct — özne türü için WHERE parçası.
//
// Dönüş: (sql, arg, uygulandı mı). arg nil ise ifade parametresizdir.
//
// hasKindCol İKİ-BOOT sözleşmesinin girdisi (v0.9.1338): kolonu EKLEYEN
// boot probe'u false okur. O boot'ta:
//   - her satır zorunlu olarak servis öznesidir (db yazan tek üretici,
//     db_capacity.go, kolon yokken kind'ı hiç YAZMAZ — problemInsertCols),
//   - dolayısıyla 'service' şeridi DARALTILMAZ (kısıt yok),
//   - 'db' şeridi SIFIR satır almalıdır. `1 = 0` yazmak, var olmayan bir
//     kolona sorgu göndermekten de, sessizce TÜM satırları döndürmekten
//     de doğrudur — ikincisi "boş küme yerine dolu küme" sınıfıdır.
func problemSubjectConjunct(subjectKind string, hasKindCol bool) (string, any, bool) {
	switch subjectKind {
	case "":
		return "", nil, false
	case ProblemKindService:
		if !hasKindCol {
			return "", nil, false
		}
		return "kind = ?", ProblemKindService, true
	default:
		// db — ve ileride eklenecek her yeni özne türü. Bilinmeyen bir
		// değer için de `kind = ?` doğru cevaptır: sıfır satır döner,
		// "filtre yokmuş gibi TÜM satırlar" DEĞİL (sessizce düşen
		// allowlist sınıfı). Çağıran zaten kapalı sözlükten normalize
		// eder; bu dal onun ikinci kapısı.
		if !hasKindCol {
			return "1 = 0", nil, true
		}
		return "kind = ?", subjectKind, true
	}
}

// problemServicesConjunct — ProblemFilter.Services daraltmasının WHERE
// parçası (v0.9.1345). SAF: yalnız uzunluk + iki bayrak.
//
// n = len(f.Services). Çağıran bu fonksiyonu YALNIZ f.Services != nil
// iken çağırır; nil "kısıt yok" demektir ve buraya hiç gelmez.
//
// allowDB — db özneli satırlar listede olmasalar da GEÇSİN (gerekçe
// ProblemFilter.ServicesAllowDBSubjects'te). hasKindCol İKİ-BOOT
// sözleşmesinin girdisi, problemSubjectConjunct ile AYNI mantık: kolonu
// EKLEYEN boot'ta `kind` yoktur, ve o boot'ta db özneli SATIR da yoktur
// (db_capacity.go kolon yokken kind'ı hiç yazmaz), dolayısıyla istisnayı
// hiç yazmamak DOĞRU cevaptır — var olmayan bir kolona sorgu göndermek
// değil.
//
// n == 0 (küme HİÇBİR ŞEYE çözüldü) + allowDB: cevap "yalnız db
// özneleri". `1 = 0` yazmak burada db satırlarını da öldürürdü — takımın
// hiç servisi yokken SADECE veritabanı sahipliği olması tam olarak
// mümkün bir hâl.
// subjectEscapeSQL — env şeridinden kaçan özne türleri: db (v0.9.1342) ve
// external (v0.10.228). İkisi de servis değildir; `service IN (...)` onları
// hiçbir env'e vermez, kaçış olmadan her env görünümünde kaybolurlardı.
// Literal GÜVENLİ: paket sabitleri, kullanıcı girdisi değil.
// İki eşitlik, IN-listesi DEĞİL: TestSubjectLaneDoesNotHideTheColumnDefault
// bu dosyada IN-liste yazımını yasaklar (DEFAULT 'service' garantisini
// gizleyebilecek sınıf) — ve kapı yorum metnini de okur. Çağıran parantezler.
const subjectEscapeSQL = "kind = '" + ProblemKindDB + "' OR kind = '" + ProblemKindExternal + "'"

func problemServicesConjunct(n int, allowDB, hasKindCol bool) string {
	dbEscape := allowDB && hasKindCol
	if n == 0 {
		// Resolved to nothing — say so in SQL rather than returning an
		// unfiltered page.
		if dbEscape {
			return "(" + subjectEscapeSQL + ")"
		}
		return "1 = 0"
	}
	in := "service IN (" + chPlaceholders(n) + ")"
	if dbEscape {
		// Literal GÜVENLİ: ProblemKindDB bir paket sabiti, kullanıcı
		// girdisi değil. Parametre bağlamak, çağıranın Services
		// argümanlarıyla sıra bağımlılığı yaratırdı.
		return "(" + in + " OR " + subjectEscapeSQL + ")"
	}
	return in
}

// CountProblemsBySubject — özne türü başına "hâlâ insan bekleyen" sayı.
//
// TEK sorgu, iki sayı: /inbox'ın şerit çipi "Veritabanı (N)" yazabilsin
// diye. `GROUP BY kind` üstünde durulması gereken tek şey, 0 SATIRLI bir
// grubun HİÇ SATIR ÜRETMEMESİ — harita önceden iki anahtarla tohumlanır,
// yoksa "db yok" ile "db ölçülemedi" ayırt edilemez (2026-08-23 dersi).
//
// exclude/envServices şekli CountProblemsNotInStatuses ile AYNI ve
// bilerek: iki sayı aynı evreni saymalı, yoksa çip ile liste ıraksar.
// Env kaçış kapıları (global satır + v0.9.1358'den beri db öznesi)
// envScopeConjunct'tan gelir — üç yüzey de aynı dizeyi üretir.
func (s *Store) CountProblemsBySubject(ctx context.Context, exclude []string, envServices []string) (map[string]uint64, error) {
	out := map[string]uint64{ProblemKindService: 0, ProblemKindDB: 0, ProblemKindExternal: 0}

	// v0.9.1358 — WHERE gövdesi rozetle ORTAK (problemCountWhere). Bu sayı
	// GROUP BY kind ile db kovasını dolduruyor: env kaçış kapısı burada
	// eksik kalsaydı çip "Veritabanı (0)" yazarken şerit satır gösterirdi.
	whereSQL, args := s.problemCountWhere(exclude, envServices)

	if !s.hasProblemKindCol {
		// Kolon henüz yok: hepsi servis öznesi. Tek COUNT yeter ve db
		// bucket'ı 0 KALIR — "ölçmedim" değil, "yok".
		n, err := s.CountProblemsNotInStatuses(ctx, exclude, envServices)
		if err != nil {
			return out, err
		}
		out[ProblemKindService] = n
		return out, nil
	}

	rows, err := s.conn.Query(ctx, `
		SELECT kind, count()
		FROM problems FINAL
		WHERE `+whereSQL+`
		GROUP BY kind
		SETTINGS max_execution_time = 5`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n uint64
		if err := rows.Scan(&k, &n); err != nil {
			return out, err
		}
		// Boş kind, okuma yolundaki ProblemSubjectKind ile AYNI anlama
		// gelir. Burada normalize etmek default'u gizlemez: WHERE değil
		// SUNUM tarafı — satırlar zaten sayıldı.
		out[ProblemSubjectKind(k)] += n
	}
	return out, rows.Err()
}
