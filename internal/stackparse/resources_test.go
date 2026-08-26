package stackparse

import "testing"

// resources_test.go — v0.10.73.
//
// Kod bağlamı bugüne dek YALNIZ stack frame'lerinin .java dosyalarını
// çekiyordu. Bir sorgu hatasında asıl kanıt çoğu zaman kodda değil,
// KAYNAK dosyasındadır (mapper XML'i, SQL parçası).
//
// ⚠ Operatörün ekranında model bir mapper XML'inden söz etti — o dosya
// HİÇ GÖNDERİLMEMİŞTİ, adı çıkarımla üretilmişti. v0.10.72'den sonra
// model bunu yapamıyor (uydurma yasak), yani dosya gerçekten
// gönderilmedikçe kanıt EKSİK kalıyor. Bu çıkarıcı o boşluğu kapatmanın
// ilk yarısı.

func TestExplicitFileNamesAreFound(t *testing.T) {
	got := ResourceRefs(`### Error querying database. Cause: bad grammar
	at parsing /WEB-INF/OrderMapper.xml line 42, see also queries.sql`)
	want := map[string]string{"OrderMapper": ".xml", "queries": ".sql"}
	if len(got) != 2 {
		t.Fatalf("aday=%d, 2 bekleniyordu: %+v", len(got), got)
	}
	for _, r := range got {
		if want[r.Base] != r.Ext {
			t.Errorf("%s için uzantı %q; beklenen %q", r.Base, r.Ext, want[r.Base])
		}
	}
}

// TestQualifiedStatementIDYieldsMapper — ASIL SİNYAL.
//
// MyBatis/iBatis hataları `com.x.y.OrderMapper.selectById` biçiminde bir
// kimlik basıyor: sondan BİR ÖNCEKİ parça mapper sınıfıdır ve dosya adı
// ona eşittir. Son parça metottur, dosya adı DEĞİLDİR.
func TestQualifiedStatementIDYieldsMapper(t *testing.T) {
	got := ResourceRefs("Error: com.example.repo.OrderMapper.selectById failed")
	if len(got) != 1 || got[0].Base != "OrderMapper" {
		t.Fatalf("mapper çıkarılamadı: %+v", got)
	}
	if got[0].Ext != "" {
		t.Errorf("nitelikli kimlikten uzantı UYDURULDU: %q", got[0].Ext)
	}
}

// TestLowercasePackagePartsAreNotCandidates — GÜRÜLTÜ ELENİR.
//
// `java.lang.String.valueOf` gibi ifadelerde sondan bir önceki parça
// büyük harfle başlar ama `com.example.repo.find` gibi durumlarda
// küçük harfli paket parçası aday olmamalı: dosya adları büyük harfle
// başlar.
func TestLowercasePackagePartsAreNotCandidates(t *testing.T) {
	for _, in := range []string{
		"at com.example.repo.find(Repo.java:10)", // sondan önceki: repo (küçük)
		"value com.a.b.c",                        // hepsi küçük
	} {
		for _, r := range ResourceRefs(in) {
			if r.Base != "" && (r.Base[0] < 'A' || r.Base[0] > 'Z') {
				t.Errorf("%q için küçük harfli aday üretildi: %q", in, r.Base)
			}
		}
	}
}

// TestOrderIsDeterministic — AYNI HATA AYNI KANITI ÜRETMELİ.
//
// Çağıran ilk N adayı çekiyor; sıra rastgele olsaydı aynı hata iki kez
// FARKLI kanıt üretirdi ve operatör hangisinin doğru olduğunu bilemezdi.
func TestOrderIsDeterministic(t *testing.T) {
	in := "ZMapper.xml AMapper.xml com.x.QMapper.sel com.x.BMapper.sel"
	first := ResourceRefs(in)
	for i := 0; i < 20; i++ {
		got := ResourceRefs(in)
		if len(got) != len(first) {
			t.Fatalf("uzunluk değişti: %d vs %d", len(got), len(first))
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("sıra değişti (%d. koşum, %d. öğe): %+v vs %+v", i, j, got[j], first[j])
			}
		}
	}
	// Açık dosya adları ÖNCE: kanıtı daha güçlü.
	if first[0].Ext == "" {
		t.Errorf("açık dosya adı önce gelmedi: %+v", first)
	}
}

func TestEmptyTextYieldsNothing(t *testing.T) {
	if got := ResourceRefs("   "); got != nil {
		t.Errorf("boş metinden aday üretildi: %+v", got)
	}
}
