package devops

// code_stats_test.go — kod-çekme sayaçlarının kapısı (v0.9.1241).
//
// SEMPTOM (denetim bulgusu): "Kodu da incele" isabet ediyor mu?
// FetchCode on bir ayrı çıkmaz sınıfı üretiyordu ama hiçbiri hiçbir
// yere yazılmıyordu — süresi dolmuş bir PAT tüm filoda kod bağlamını
// sessizce kapatabilir, açıklamalar kodsuz üretilmeye devam eder
// (fail-open) ve hiçbir ekranda tek bir iz kalmazdı.
//
// Kapı İKİ katmanda ısırıyor, bilerek:
//   (1) sayaç bloğunun kendisi — her sınıf TAM OLARAK kendi kovasını
//       artırır, bilinmeyen sınıf "other"a düşer, eşzamanlı güvenli;
//   (2) BAĞLANMA — gerçek FetchCode çağrıları (sahte TFS ile) beklenen
//       sınıfı üretir. Yalnız (1) yazılsaydı, çıkışlardan birine sınıf
//       atamayı unutmak testi hiç kırmazdı: saf test ≠ bağlanma testi.

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/cilcenk/coremetry/internal/stackparse"
)

// missCount — snapshot'ta bir çıkmaz kovasının sayısı (yoksa 0).
func missCount(s CodeStats, class CodeOutcome) int64 {
	for _, m := range s.Misses {
		if m.Class == string(class) {
			return m.Count
		}
	}
	return 0
}

// TestCodeObsBucketsAreExact — her sınıf TAM OLARAK kendi kovasını
// artırır. Tablo, sınıf kümesinin TAMAMINI dolaşıyor: yeni bir sınıf
// eklenip tabloya yazılmazsa alttaki kapsam assert'i kırılır.
func TestCodeObsBucketsAreExact(t *testing.T) {
	all := []CodeOutcome{CodeOK, CodePartial}
	all = append(all, codeMissClasses[:]...)

	for _, class := range all {
		t.Run(string(class), func(t *testing.T) {
			var o codeObs
			o.record(class, "gerekçe")
			got := o.snapshot()

			if got.Attempts != 1 {
				t.Fatalf("Attempts=%d, istenen 1", got.Attempts)
			}
			wantOK, wantPartial := int64(0), int64(0)
			switch class {
			case CodeOK:
				wantOK = 1
			case CodePartial:
				wantPartial = 1
			}
			if got.OK != wantOK || got.Partial != wantPartial {
				t.Fatalf("OK=%d Partial=%d, istenen %d/%d", got.OK, got.Partial, wantOK, wantPartial)
			}
			// İsabet kovaları çıkmaz üretmez, çıkmazlar isabet üretmez.
			if wantOK+wantPartial > 0 {
				if len(got.Misses) != 0 {
					t.Fatalf("isabet çıkmaz kovası doldurdu: %+v", got.Misses)
				}
				if got.LastError != "" {
					t.Fatalf("isabet LastError yazdı: %q", got.LastError)
				}
			} else {
				if n := missCount(got, class); n != 1 {
					t.Fatalf("%s kovası=%d, istenen 1 (kovalar: %+v)", class, n, got.Misses)
				}
				if len(got.Misses) != 1 {
					t.Fatalf("tek deneme birden çok kova doldurdu: %+v", got.Misses)
				}
				if got.LastError != "gerekçe" || got.LastErrorUnix == 0 {
					t.Fatalf("LastError=%q at=%d — çıkmazın gerekçesi taşınmadı",
						got.LastError, got.LastErrorUnix)
				}
			}
			if got.LastOutcome != string(class) {
				t.Fatalf("LastOutcome=%q, istenen %q", got.LastOutcome, class)
			}
			if got.LastUnix == 0 {
				t.Fatal("LastUnix yazılmadı — 'ne zaman denendi' cevapsız kalır")
			}
		})
	}
}

// TestCodeObsUnknownClassGoesToOther — sınıflandırılmamış bir çıkış
// SESSİZCE KAYBOLMAZ ve asla "ok" sayılmaz. Bu tam olarak FetchCode'un
// varsayılan sınıfının ("other") koruduğu hâl: sınıf atamayan yeni bir
// dal eklenirse isabet oranı yalan söylemesin, kova görünsün.
func TestCodeObsUnknownClassGoesToOther(t *testing.T) {
	var o codeObs
	o.record(CodeOutcome("yepyeni-dal"), "bilinmeyen")
	got := o.snapshot()

	if got.OK != 0 || got.Partial != 0 {
		t.Fatalf("bilinmeyen sınıf isabet sayıldı: OK=%d Partial=%d", got.OK, got.Partial)
	}
	if n := missCount(got, CodeOther); n != 1 {
		t.Fatalf("other kovası=%d, istenen 1 (kovalar: %+v)", n, got.Misses)
	}
	if got.LastOutcome != string(CodeOther) {
		t.Fatalf("LastOutcome=%q, istenen %q", got.LastOutcome, CodeOther)
	}
}

// TestCodeObsLastErrorIsSticky — sonraki bir BAŞARI son hatayı SİLMEZ.
// Gerekçe v0.9.1077'nin dersiyle aynı: flap eden bir arızayı tek şanslı
// isabet ekrandan silerse operatör onu hiç görmez. Tazeliği zaman
// damgası anlatır, silmek değil.
func TestCodeObsLastErrorIsSticky(t *testing.T) {
	var o codeObs
	o.record(CodeBackendError, "TF400813: yetkisiz")
	o.record(CodeOK, "")
	got := o.snapshot()

	if got.LastOutcome != string(CodeOK) {
		t.Fatalf("LastOutcome=%q, istenen ok — SON denemenin sınıfı taşınmalı", got.LastOutcome)
	}
	if got.LastError != "TF400813: yetkisiz" {
		t.Fatalf("LastError=%q — başarı, önceki arızayı sildi", got.LastError)
	}
	if got.Attempts != 2 || got.OK != 1 || missCount(got, CodeBackendError) != 1 {
		t.Fatalf("sayaçlar tutmadı: %+v", got)
	}
}

// TestCodeObsMissesSortedAndSparse — kovalar ÇOKTAN AZA sıralı, sıfır
// olanlar hiç yok. Panel her yenilemede zıplamamalı; on bir boş kova da
// asıl sinyali gömerdi.
func TestCodeObsMissesSortedAndSparse(t *testing.T) {
	var o codeObs
	for i := 0; i < 5; i++ {
		o.record(CodeTreeMiss, "ıska")
	}
	for i := 0; i < 2; i++ {
		o.record(CodeDeadline, "tavan")
	}
	o.record(CodeEmptyTree, "boş ağaç")
	got := o.snapshot()

	if len(got.Misses) != 3 {
		t.Fatalf("kova sayısı=%d, istenen 3 (sıfırlar gelmemeli): %+v", len(got.Misses), got.Misses)
	}
	want := []string{string(CodeTreeMiss), string(CodeDeadline), string(CodeEmptyTree)}
	for i, w := range want {
		if got.Misses[i].Class != w {
			t.Fatalf("sıra[%d]=%s, istenen %s (çoktan aza): %+v", i, got.Misses[i].Class, w, got.Misses)
		}
	}
	if got.Attempts != 8 {
		t.Fatalf("Attempts=%d, istenen 8", got.Attempts)
	}
}

// TestCodeObsTieOrderIsDeterministic — eşit sayıda iki kova her
// snapshot'ta AYNI sırada gelmeli (sınıf adına göre).
func TestCodeObsTieOrderIsDeterministic(t *testing.T) {
	var o codeObs
	o.record(CodeTreeMiss, "")
	o.record(CodeDeadline, "")
	first := o.snapshot()
	for i := 0; i < 10; i++ {
		got := o.snapshot()
		for j := range got.Misses {
			if got.Misses[j] != first.Misses[j] {
				t.Fatalf("sıra deterministik değil: %+v vs %+v", got.Misses, first.Misses)
			}
		}
	}
	if first.Misses[0].Class != string(CodeDeadline) {
		t.Fatalf("eşitlikte sınıf adı sıralamıyor: %+v", first.Misses)
	}
}

// TestCodeObsConcurrent — `go test -race` altında koşar. Sayaç bloğu
// istek yolunda paylaşılıyor: iki eşzamanlı açıklama isteği aynı
// Service'i kullanır.
func TestCodeObsConcurrent(t *testing.T) {
	var o codeObs
	const workers, each = 8, 200
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				switch i % 4 {
				case 0:
					o.record(CodeOK, "")
				case 1:
					o.record(CodePartial, "eksik")
				case 2:
					o.record(CodeTreeMiss, "ıska")
				default:
					o.record(CodeBackendError, "401")
				}
			}
		}(w)
	}
	wg.Wait()
	got := o.snapshot()

	total := int64(workers * each)
	if got.Attempts != total {
		t.Fatalf("Attempts=%d, istenen %d — sayaç kaybı", got.Attempts, total)
	}
	q := total / 4
	if got.OK != q || got.Partial != q ||
		missCount(got, CodeTreeMiss) != q || missCount(got, CodeBackendError) != q {
		t.Fatalf("kova dağılımı bozuk (her biri %d olmalı): %+v", q, got)
	}
}

// TestCodeStatsHitRate — saf oran. "Hiç denenmedi" (0) ile "%0 isabet"
// aynı şey değil; oran 0 döner ama çağıran Attempts'i ayrı gösterir.
func TestCodeStatsHitRate(t *testing.T) {
	tests := []struct {
		name string
		in   CodeStats
		want float64
	}{
		{"deneme yok", CodeStats{}, 0},
		{"tam isabet", CodeStats{Attempts: 4, OK: 4}, 1},
		{"kısmi de isabettir", CodeStats{Attempts: 4, OK: 1, Partial: 1}, 0.5},
		{"hiç isabet yok", CodeStats{Attempts: 3}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.HitRate(); got != tt.want {
				t.Fatalf("HitRate=%v, istenen %v", got, tt.want)
			}
		})
	}
}

// TestCodeObservabilityNilSafe — kod entegrasyonu hiç kurulmamış bir
// süreçte çağıran ekstra muhafız yazmak zorunda kalmamalı.
func TestCodeObservabilityNilSafe(t *testing.T) {
	var s *Service
	s.RecordCodeOutcome(CodeBackendError, "patlamamalı")
	if got := s.CodeObservability(); got.Attempts != 0 || len(got.Misses) != 0 {
		t.Fatalf("nil Service'ten sayaç geldi: %+v", got)
	}
}

// TestFetchCodeRecordsOutcome — BAĞLANMA kapısı. Gerçek FetchCode
// çağrıları (sahte TFS) beklenen sınıfı üretir. Saf sayaç testi bunu
// göremezdi: çıkışlardan birine sınıf atamayı unutmak orada hiçbir şeyi
// kırmaz, burada kırar.
func TestFetchCodeRecordsOutcome(t *testing.T) {
	const hitPath = "/src/main/java/com/example/card/CardDetailBusiness.java"
	const otherPath = "/src/main/java/com/example/card/CardRepository.java"

	// İki uygulama frame'i: ilki ağaçta VAR, ikincisi duruma göre.
	stack := "" +
		"jakarta.ejb.EJBException: host response error\n" +
		"\tat com.example.card.CardDetailBusiness.handle(CardDetailBusiness.java:246)\n" +
		"\tat com.example.card.CardRepository.find(CardRepository.java:88)\n"
	frames := stackparse.ParseJava(stack)

	tests := []struct {
		name   string
		setup  func(f *fakeTFS, cfg *Settings)
		repo   string
		hint   ProjectHint
		frames []stackparse.Frame
		want   CodeOutcome
	}{
		{
			name: "iki frame de bulundu → ok",
			setup: func(f *fakeTFS, _ *Settings) {
				f.tree = []string{hitPath, otherPath}
				f.files[hitPath] = javaFile("com.example.card", "CardDetailBusiness", 400, 246)
				f.files[otherPath] = javaFile("com.example.card", "CardRepository", 200, 88)
			},
			repo: "core-service", frames: frames, want: CodeOK,
		},
		{
			name: "bir frame ağaçta yok → partial",
			setup: func(f *fakeTFS, _ *Settings) {
				f.tree = []string{hitPath}
				f.files[hitPath] = javaFile("com.example.card", "CardDetailBusiness", 400, 246)
			},
			repo: "core-service", frames: frames, want: CodePartial,
		},
		{
			name: "hiçbir frame ağaçta yok → tree-miss",
			setup: func(f *fakeTFS, _ *Settings) {
				f.tree = []string{"/src/main/java/com/example/other/Thing.java"}
			},
			repo: "core-service", frames: frames, want: CodeTreeMiss,
		},
		{
			name:  "ağaç boş → empty-tree",
			setup: func(f *fakeTFS, _ *Settings) { f.tree = nil },
			repo:  "core-service", frames: frames, want: CodeEmptyTree,
		},
		{
			name: "PAT reddedildi → backend-error",
			setup: func(f *fakeTFS, cfg *Settings) {
				f.tree = []string{hitPath}
				cfg.PAT = ""
			},
			repo: "core-service", frames: frames, want: CodeBackendError,
		},
		{
			name:   "dosya+satır taşıyan uygulama frame'i yok → no-stack",
			setup:  func(f *fakeTFS, _ *Settings) { f.tree = []string{hitPath} },
			repo:   "core-service",
			frames: stackparse.ParseJava("java.lang.RuntimeException: boş\n"),
			want:   CodeNoStack,
		},
		{
			name:  "depo adı boş → repo-unresolved",
			setup: func(f *fakeTFS, _ *Settings) { f.tree = []string{hitPath} },
			repo:  "", frames: frames, want: CodeRepoUnresolved,
		},
		{
			name: "bağlantı yapılandırılmamış → unconfigured",
			setup: func(f *fakeTFS, cfg *Settings) {
				cfg.BaseURL = ""
			},
			repo: "core-service", frames: frames, want: CodeUnconfigured,
		},
		{
			name: "proje hiçbir kaynaktan türemiyor → project-dead-end",
			setup: func(f *fakeTFS, cfg *Settings) {
				f.tree = []string{hitPath}
				cfg.Project = ""
			},
			repo: "core-service", frames: frames, want: CodeProjectDeadEnd,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeTFS(t)
			f.files = map[string]string{}
			cfg := f.settings()
			tt.setup(f, &cfg)

			svc := New()
			svc.Configure(cfg)
			cc := svc.FetchCode(context.Background(), tt.repo, tt.hint, tt.frames)

			got := svc.CodeObservability()
			if got.Attempts != 1 {
				t.Fatalf("Attempts=%d, istenen 1 — her çağrı TAM BİR kez sayılmalı", got.Attempts)
			}
			if got.LastOutcome != string(tt.want) {
				t.Fatalf("sınıf=%q, istenen %q (Reason: %s)", got.LastOutcome, tt.want, cc.Reason)
			}
			switch tt.want {
			case CodeOK:
				if got.OK != 1 || len(got.Misses) != 0 {
					t.Fatalf("ok kovası tutmadı: %+v", got)
				}
			case CodePartial:
				if got.Partial != 1 || len(got.Misses) != 0 {
					t.Fatalf("partial kovası tutmadı: %+v", got)
				}
			default:
				if missCount(got, tt.want) != 1 {
					t.Fatalf("%s kovası dolmadı: %+v", tt.want, got)
				}
				// Çıkmazın gerekçesi ekranda görünmeli — boş bir
				// "başarısız" satırı operatöre hiçbir şey söylemez.
				if strings.TrimSpace(got.LastError) == "" {
					t.Fatalf("LastError boş — çıkmazın gerekçesi taşınmadı (Reason: %q)", cc.Reason)
				}
			}
		})
	}
}

// TestFetchCodeCountsEveryCall — ardışık çağrılar BİRİKİR. Sayaç tek
// çağrıda doğru olup ikincisinde susarsa "isabet oranı" hiçbir zaman
// anlamlı bir örneklem toplayamaz.
func TestFetchCodeCountsEveryCall(t *testing.T) {
	f := newFakeTFS(t)
	const path = "/src/main/java/com/example/card/CardDetailBusiness.java"
	f.tree = []string{path}
	f.files[path] = javaFile("com.example.card", "CardDetailBusiness", 400, 246)

	svc := New()
	svc.Configure(f.settings())
	frames := stackparse.ParseJava(
		"\tat com.example.card.CardDetailBusiness.handle(CardDetailBusiness.java:246)\n")

	for i := 0; i < 3; i++ {
		svc.FetchCode(context.Background(), "core-service", ProjectHint{}, frames)
	}
	got := svc.CodeObservability()
	if got.Attempts != 3 || got.OK != 3 {
		t.Fatalf("Attempts=%d OK=%d, istenen 3/3", got.Attempts, got.OK)
	}
	if got.HitRate() != 1 {
		t.Fatalf("HitRate=%v, istenen 1", got.HitRate())
	}
}
