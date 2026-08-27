package stackparse

import (
	"reflect"
	"testing"
)

// rank_test.go — v0.10.112. UYGULAMA FRAME'İ ÖNCE.
//
// Operatör teşhisi (2026-08-28): "deneme tavanı (6) doldu — 4 frame
// denenmedi; denenmeyenler arasında uygulama sınıfları, denenenler
// arasında framework sınıfları (RestBackendExecutor, BasicDispatcher,
// RestFilter)". Kurum-içi çerçeve sınıfları `frameworkPrefixes`'te
// olmadığı için IsApp=true sayılıyor ve stack'te iş sınıfından ÖNCE
// geldikleri için tavanı önce onlar yiyordu. Pozitif bir uygulama-önek
// listesi yoktu; RankFrames onu getiriyor.

func rf(class, file string, line, seg int) Frame {
	return Frame{Class: class, Method: "m", File: file, Line: line, Segment: seg, IsApp: IsAppClass(class)}
}

func classes(fs []Frame) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Class)
	}
	return out
}

// TestRankFramesAppPrefixFirst — OPERATÖRÜN BİLDİRDİĞİ DURUM: kurum-içi
// çerçeve stack'te önce, iş sınıfı sonda. Önek listesiyle iş sınıfı başa
// gelir; çerçeve sınıfları ATILMAZ, arkaya düşer (yine denenebilir).
func TestRankFramesAppPrefixFirst(t *testing.T) {
	frames := []Frame{
		rf("com.acme.core.rest.RestFilter", "RestFilter.java", 10, 0),
		rf("com.acme.core.rest.BasicDispatcher", "BasicDispatcher.java", 20, 0),
		rf("com.acme.core.rest.RestBackendExecutor", "RestBackendExecutor.java", 30, 0),
		rf("com.acme.billing.CardService", "CardService.java", 29, 0),
		rf("com.acme.billing.CardRepository", "CardRepository.java", 88, 0),
	}
	got := classes(RankFrames(frames, 10, []string{"com.acme.billing."}))
	want := []string{
		"com.acme.billing.CardService", "com.acme.billing.CardRepository",
		"com.acme.core.rest.RestFilter", "com.acme.core.rest.BasicDispatcher", "com.acme.core.rest.RestBackendExecutor",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sıra yanlış:\n got %v\nwant %v", got, want)
	}
	// Tavan sıralamadan SONRA uygulanır: n=2 iki iş sınıfını verir.
	if got := classes(RankFrames(frames, 2, []string{"com.acme.billing."})); !reflect.DeepEqual(got, want[:2]) {
		t.Errorf("n=2: %v", got)
	}
	// Tier damgası: iş sınıfı 0, kurum-içi çerçeve 1.
	ranked := RankFrames(frames, 10, []string{"com.acme.billing."})
	if ranked[0].Tier != 0 || ranked[2].Tier != 1 {
		t.Errorf("tier damgası: %+v", ranked)
	}
}

// TestRankFramesEmptyPrefixesIsAppFrames — önek yoksa davranış BİRE BİR
// eski (AppFrames): en derin segment önce, segment içinde metin sırası.
func TestRankFramesEmptyPrefixesIsAppFrames(t *testing.T) {
	frames := []Frame{
		rf("com.x.Wrapper", "Wrapper.java", 5, 0),
		rf("org.springframework.web.Dispatcher", "Dispatcher.java", 9, 0),
		rf("com.x.Inner", "Inner.java", 7, 1),
		rf("com.x.Root", "Root.java", 3, 2),
		rf("com.x.NoLine", "NoLine.java", 0, 2),
	}
	for _, prefixes := range [][]string{nil, {}, {"", "  "}} {
		got := RankFrames(frames, 10, prefixes)
		want := AppFrames(frames, 10)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("önek %v: RankFrames %v ≠ AppFrames %v", prefixes, classes(got), classes(want))
		}
	}
	if RankFrames(frames, 0, []string{"com.x."}) != nil {
		t.Error("n=0 nil dönmeli")
	}
}

// TestRankFramesTierBeatsSegmentButSegmentOrdersWithinTier — birincil
// anahtar tier, ikincil segment derinliği: derin segmentteki çerçeve
// sınıfı, dış segmentteki iş sınıfının ARKASINA düşer; iki iş sınıfı
// arasında ise derin olan önde kalır (v0.9.1235 sözleşmesi korunur).
func TestRankFramesTierBeatsSegmentButSegmentOrdersWithinTier(t *testing.T) {
	frames := []Frame{
		rf("com.acme.app.Outer", "Outer.java", 1, 0),
		rf("com.acme.core.Deep", "Deep.java", 2, 2),
		rf("com.acme.app.Root", "Root.java", 3, 2),
		rf("com.acme.app.Mid", "Mid.java", 4, 1),
	}
	got := classes(RankFrames(frames, 10, []string{"com.acme.app."}))
	want := []string{"com.acme.app.Root", "com.acme.app.Mid", "com.acme.app.Outer", "com.acme.core.Deep"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("\n got %v\nwant %v", got, want)
	}
}

// TestRankFramesPrefixSemantics — ÖNEK eşleşmesi, "içeriyor" değil;
// operatörün açık öneki JDK/çerçeve listesini de EZER (org.apache.myco
// bir uygulama olabilir); konumlandırılamayan frame yine elenir.
func TestRankFramesPrefixSemantics(t *testing.T) {
	frames := []Frame{
		rf("xcom.acme.Fake", "Fake.java", 1, 0),
		rf("org.apache.myco.Job", "Job.java", 2, 0),
		rf("org.apache.commons.io.IOUtils", "IOUtils.java", 3, 0),
		rf("com.acme.Real", "Real.java", 4, 0),
		rf("com.acme.NoFile", "", 0, 0),
	}
	got := classes(RankFrames(frames, 10, []string{"com.acme", "org.apache.myco."}))
	want := []string{"org.apache.myco.Job", "com.acme.Real", "xcom.acme.Fake"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("\n got %v\nwant %v", got, want)
	}
	if !HasAppPrefix("com.acme.Real", []string{"com.acme"}) || HasAppPrefix("xcom.acme.Fake", []string{"com.acme"}) {
		t.Error("HasAppPrefix önek semantiği bozuk")
	}
}
