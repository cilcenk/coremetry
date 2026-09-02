package logql

import (
	"strings"
	"testing"
)

// parse_test.go — v0.10.279. Tablo-güdümlü, stdlib. Kapsam docs/audit/
// log-search.md §6.4: örtük AND · NOT önceliği · iç içe parantez ·
// tırnaklı değerde iki nokta · _exists_ · key:(a OR b) · key:>=v · kaçışlı
// tırnak · aralık · joker · hata konumları.
//
// Beklenen değer AST'nin kanonik String() yazımı — ağaç şeklini tek satırda
// pinler ve Parse(String(e)) yeniden aynı ağacı vermek zorundadır
// (TestRoundTrip).

func TestParseCanonical(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"boş", "", ""},
		{"yalnız boşluk", "   ", ""},
		{"tek terim", "timeout", "timeout"},
		{"örtük AND", "connection refused", "connection AND refused"},
		{"tırnaklı ifade tek yaprak", `"connection refused"`, `"connection refused"`},
		{"tırnak içinde iki nokta değerdir", `"connection refused: timeout"`, `"connection refused: timeout"`},
		{"alan + tırnaklı değer (compileSearch)", `service.name:"checkout"`, `service.name:"checkout"`},
		{"alan + çıplak değer", `level:error`, `level:error`},
		{"küçük harf and/or/not", `a and b or not c`, `(a AND b) OR NOT c`},
		{"OR AND önceliği", `a OR b AND c`, `a OR (b AND c)`},
		{"NOT en sıkı", `NOT a AND b`, `NOT a AND b`},
		{"NOT parantez", `NOT (a OR b)`, `NOT (a OR b)`},
		{"çift NOT sadeleşir", `NOT NOT a`, `a`},
		{"eksi öneki NOT", `-level:debug body`, `NOT level:debug AND body`},
		{"ünlem NOT", `!a b`, `NOT a AND b`},
		{"terim içi tire NOT değil", `k8s-pod-7f`, `k8s-pod-7f`},
		{"iç içe parantez", `((a OR b) AND (c OR d))`, `(a OR b) AND (c OR d)`},
		{"compileSearch pill + serbest metin OR parantezli", `service.name:"db" AND (disk OR memory)`, `service.name:"db" AND (disk OR memory)`},
		{"_exists_", `_exists_:trace_id`, `_exists_:trace_id`},
		{"NOT _exists_", `NOT _exists_:k8s.pod.name`, `NOT _exists_:k8s.pod.name`},
		{"key:* varlık", `error.type:*`, `_exists_:error.type`},
		{"is-one-of", `channel:("MOBILE" OR "WEB")`, `channel:"MOBILE" OR channel:"WEB"`},
		{"is-one-of NOT", `NOT channel:("MOBILE" OR "WEB")`, `NOT (channel:"MOBILE" OR channel:"WEB")`},
		{"alan grubu AND NOT", `level:(error AND NOT warn)`, `level:error AND NOT level:warn`},
		{"gte tırnaklı", `http.status:>="500"`, `http.status:>="500"`},
		{"lte çıplak", `duration_ms:<=250`, `duration_ms:<=250`},
		{"gt lt", `a:>1 b:<2`, `a:>1 AND b:<2`},
		{"aralık kapalı", `status:[400 TO 499]`, `status:[400 TO 499]`},
		{"aralık açık uç", `ts:{* TO 2026-09-01}`, `ts:{* TO 2026-09-01}`},
		{"aralık karışık ayraç", `n:[1 TO 5}`, `n:[1 TO 5}`},
		{"joker", `pod:api-*`, `pod:api-*`},
		{"soru işareti joker", `host:node?`, `host:node?`},
		{"kaçışlı tırnak değerde", `msg:"say \"hi\""`, `msg:"say \"hi\""`},
		{"kaçışlı ters bölü", `path:"C:\\tmp"`, `path:"C:\\tmp"`},
		{"saat düz metindir", `12:30 restart`, `12:30 AND restart`},
		{"ERROR: boom düz metin", `ERROR: boom`, `ERROR: AND boom`},
		{"http://host şema düz metin", `http://host/x`, `http://host/x`},
		{"&& ||", `a && b || c`, `(a AND b) OR c`},
		{"alan adı @ ve tire", `@timestamp:>=x k8s-label:v`, `@timestamp:>=x AND k8s-label:v`},
		{"nokta zinciri", `kubernetes.labels.app:api`, `kubernetes.labels.app:api`},
		{"büyük harf operatör kelimesi terim değil", `AND:x`, `AND:x`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := Parse(tc.in)
			if err != nil {
				t.Fatalf("Parse(%q) hata: %v", tc.in, err)
			}
			if got := e.String(); got != tc.want {
				t.Errorf("Parse(%q).String() = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct {
		name, in, msg string
		pos           int
	}{
		{"kapanmamış tırnak", `a "b`, "kapanmamış tırnak", 2},
		{"kapanmamış parantez", `(a OR b`, "')' bekleniyordu", 7},
		{"fazla kapanış", `a) b`, "beklenmeyen ')'", 1},
		{"boş parantez", `a ()`, "boş parantez", 2},
		{"sonda AND", `a AND`, "ifade bekleniyordu", 5},
		{"başta OR", `OR a`, "beklenmeyen 'OR'", 0},
		{"NOT tek başına", `NOT`, "sonrasında ifade yok", 0},
		{"aralık TO eksik", `n:[1 5]`, "'TO' bekleniyordu", 5},
		{"tek başına yıldız", `*`, "tek başına '*'", 0},
		{"alan içinde alan", `a:(b:c)`, "alan içinde alan", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.in)
			if err == nil {
				t.Fatalf("Parse(%q) hata vermedi", tc.in)
			}
			pe, ok := err.(*ParseError)
			if !ok {
				t.Fatalf("hata tipi %T; *ParseError bekleniyordu", err)
			}
			if !strings.Contains(pe.Msg, tc.msg) {
				t.Errorf("Parse(%q) mesajı %q; %q içermeliydi", tc.in, pe.Msg, tc.msg)
			}
			if pe.Pos != tc.pos {
				t.Errorf("Parse(%q) konum %d; want %d", tc.in, pe.Pos, tc.pos)
			}
		})
	}
	// `level:` — alan öneki kuralı: `:` ardından sorgu sonu → alan DEĞİL,
	// çıplak terim "level:" olarak okunur (fieldQueryRe ile aynı karar).
	e, err := Parse(`level:`)
	if err != nil {
		t.Fatalf("level: hata: %v", err)
	}
	if e.Kind != KindClause || e.Field != "" || e.Value != "level:" {
		t.Errorf("`level:` çıplak terim olmalıydı: %+v", e)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, in := range []string{
		`service.name:"checkout" AND (disk OR memory) AND NOT level:debug`,
		`channel:("MOBILE" OR "WEB") AND http.status:>="500"`,
		`status:[400 TO 499] OR _exists_:error.type`,
		`msg:"say \"hi\"" pod:api-*`,
	} {
		e, err := Parse(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		s := e.String()
		e2, err := Parse(s)
		if err != nil {
			t.Fatalf("String() yeniden ayrışmadı %q: %v", s, err)
		}
		if e2.String() != s {
			t.Errorf("round-trip kararsız: %q → %q → %q", in, s, e2.String())
		}
	}
}

func TestHelpers(t *testing.T) {
	e, _ := Parse(`a service.name:"x" AND (level:error OR pod:api-*) NOT service.name:"y"`)
	if !HasFieldClause(e) {
		t.Error("HasFieldClause false")
	}
	if got := strings.Join(Fields(e), ","); got != "service.name,level,pod" {
		t.Errorf("Fields = %q", got)
	}
	free, _ := Parse(`just text "and phrase"`)
	if HasFieldClause(free) {
		t.Error("serbest metin alan yüklemi taşımıyor")
	}
	if Fields(nil) != nil || HasFieldClause(nil) {
		t.Error("nil ifade")
	}
}
