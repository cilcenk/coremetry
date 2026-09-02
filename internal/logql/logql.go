// Package logql — /logs arama dilinin (Lucene / KQL alt kümesi) TEK
// ayrıştırıcısı (v0.10.279, docs/audit/log-search.md §4.1).
//
// Bugüne kadar sorgu frontend'de dize olarak derlendi (lib/logFilters.ts
// compileSearch) ve arka uca verbatim geçti. Elasticsearch bunu
// query_string ile gerçek alan sorgusu olarak anlıyordu; ClickHouse
// (VARSAYILAN arka uç) aynı dizeyi log GÖVDESİNDE alt-dize olarak arıyordu
// — `service.name:"checkout"` yapısal olarak 0 satır, hata yok
// (logstore/query_syntax.go'daki ölçüm). Bu paket o dizeyi bir AST'ye
// çevirir; ClickHouse derleyicisi (Compile) alan yüklemlerini gerçek
// kolon/res-array ifadelerine bağlar.
//
// Gramer (Discover paritesi, fazlası değil):
//
//	expr    := or
//	or      := and ("OR" and)*
//	and     := not (("AND" | ε) not)*          örtük AND (ES default_operator=AND)
//	not     := ("NOT" | "-" | "!")? primary
//	primary := "(" expr ")" | clause
//	clause  := field ":" fieldValue | term | phrase
//	fieldValue := value | "(" expr ")" | range | "*"
//	range   := (">=" | "<=" | ">" | "<") value | ("[" | "{") value "TO" value ("]" | "}")
//	field   := "_exists_" | ident ("." ident)*
//
// Sözleşme:
//   - Tırnaklı değer LİTERAL (joker yok, iki nokta değer parçası).
//   - Çıplak terimde `*`/`?` joker.
//   - `key:(a OR b)` — alan, parantezin içindeki her yaprağa dağıtılır
//     (`key:(a AND NOT b)` de geçerli).
//   - `_exists_:key` ve `key:*` aynı şey: varlık yüklemi.
//   - Boş sorgu → nil ifade, hata yok.
//   - Hata konumu bayt ofsetidir; mesaj operatöre gösterilebilir.
//
// Bağımlılığı yok: chstore bu paketi import eder, tersi değil.
package logql

import (
	"fmt"
	"strings"
)

// Kind — düğüm türü.
type Kind int

const (
	KindAnd Kind = iota + 1
	KindOr
	KindNot
	KindClause
)

// Op — yaprak (clause) operatörü.
type Op int

const (
	OpMatch  Op = iota // alan = değer (alan boşsa gövde araması)
	OpGt               // key:>v
	OpGte              // key:>=v
	OpLt               // key:<v
	OpLte              // key:<=v
	OpExists           // _exists_:key / key:*
	OpRange            // key:[a TO b] / key:{a TO b}
)

// Expr — AST düğümü. And/Or ≥2 çocuk, Not tek çocuk, Clause yaprak.
type Expr struct {
	Kind Kind
	Kids []*Expr

	// Yaprak alanları (Kind == KindClause).
	Field    string // "" = serbest metin (gövde)
	Op       Op
	Value    string // OpMatch / karşılaştırma değeri
	Phrase   bool   // tırnaklı → literal
	Wildcard bool   // çıplak terimde * veya ?
	// OpRange
	Lo, Hi         string // "" veya "*" = açık uç
	LoIncl, HiIncl bool
}

// ParseError — konumlu sözdizimi hatası; Error() operatöre gösterilebilir.
type ParseError struct {
	Pos int
	Msg string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("sorgu sözdizimi (konum %d): %s", e.Pos, e.Msg)
}

// ── tokenizer ────────────────────────────────────────────────────────────

type tokKind int

const (
	tEOF tokKind = iota
	tLParen
	tRParen
	tLBracket // [
	tLBrace   // {
	tRBracket // ]
	tRBrace   // }
	tAnd
	tOr
	tNot
	tTo
	tTerm   // çıplak terim (kaçışlar çözülmüş)
	tPhrase // tırnaklı (kaçışlar çözülmüş)
	tField  // ident ":" — alan öneki (metin, ":" hariç)
	tCmp    // >= <= > <
)

type token struct {
	kind tokKind
	text string
	pos  int
	// raw — çıplak terimde kaçışsız ham metin; joker tespiti için
	// (kaçışlı `\*` joker DEĞİLDİR).
	wild bool
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '@'
}

// fieldPrefixLen — s[i:] bir alan öneki ile başlıyorsa `:`'ye kadar olan
// uzunluk, değilse 0. Kural query_syntax.go'nun fieldQueryRe'siyle aynı:
// harf/altçizgi ile başlar, `:` ardından boşluk YA DA `/` gelemez
// (`ERROR: boom` düz metin, `http://host` şema).
func fieldPrefixLen(s string, i int) int {
	if i >= len(s) || !isIdentStart(s[i]) {
		return 0
	}
	j := i + 1
	for j < len(s) && isIdentChar(s[j]) {
		j++
	}
	if j >= len(s) || s[j] != ':' {
		return 0
	}
	if j+1 >= len(s) || isSpace(s[j+1]) || s[j+1] == '/' {
		return 0
	}
	return j - i
}

func lex(s string) ([]token, error) {
	var out []token
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case isSpace(c):
			i++
			continue
		case c == '(':
			out = append(out, token{kind: tLParen, pos: i})
			i++
			continue
		case c == ')':
			out = append(out, token{kind: tRParen, pos: i})
			i++
			continue
		case c == '[':
			out = append(out, token{kind: tLBracket, pos: i})
			i++
			continue
		case c == '{':
			out = append(out, token{kind: tLBrace, pos: i})
			i++
			continue
		case c == ']':
			out = append(out, token{kind: tRBracket, pos: i})
			i++
			continue
		case c == '}':
			out = append(out, token{kind: tRBrace, pos: i})
			i++
			continue
		case c == '"':
			j := i + 1
			var b strings.Builder
			closed := false
			for j < len(s) {
				if s[j] == '\\' && j+1 < len(s) {
					b.WriteByte(s[j+1])
					j += 2
					continue
				}
				if s[j] == '"' {
					closed = true
					break
				}
				b.WriteByte(s[j])
				j++
			}
			if !closed {
				return nil, &ParseError{Pos: i, Msg: "kapanmamış tırnak"}
			}
			out = append(out, token{kind: tPhrase, text: b.String(), pos: i})
			i = j + 1
			continue
		case c == '>' || c == '<':
			if i+1 < len(s) && s[i+1] == '=' {
				out = append(out, token{kind: tCmp, text: s[i : i+2], pos: i})
				i += 2
			} else {
				out = append(out, token{kind: tCmp, text: s[i : i+1], pos: i})
				i++
			}
			continue
		case c == '&' && i+1 < len(s) && s[i+1] == '&':
			out = append(out, token{kind: tAnd, text: "&&", pos: i})
			i += 2
			continue
		case c == '|' && i+1 < len(s) && s[i+1] == '|':
			out = append(out, token{kind: tOr, text: "||", pos: i})
			i += 2
			continue
		case c == '!' && i+1 < len(s) && !isSpace(s[i+1]):
			out = append(out, token{kind: tNot, text: "!", pos: i})
			i++
			continue
		case c == '-' && i+1 < len(s) && !isSpace(s[i+1]) && (i == 0 || isSpace(s[i-1]) || s[i-1] == '('):
			// Lucene `-term` = NOT term. Terim içindeki `-` (k8s-pod) buraya
			// düşmez: yalnız sorgu/parantez başında ya da boşluktan sonra.
			out = append(out, token{kind: tNot, text: "-", pos: i})
			i++
			continue
		}
		if n := fieldPrefixLen(s, i); n > 0 {
			out = append(out, token{kind: tField, text: s[i : i+n], pos: i})
			i += n + 1 // ':' atla
			continue
		}
		// Çıplak terim: boşluk / parantez / köşeli-süslü / tırnak'a kadar;
		// `\x` kaçışı çözülür. `>`/`<` terim içinde kalabilir (`a<b`).
		j := i
		var b strings.Builder
		wild := false
		for j < len(s) {
			d := s[j]
			if isSpace(d) || d == '(' || d == ')' || d == '"' || d == '[' || d == ']' || d == '{' || d == '}' {
				break
			}
			if d == '\\' && j+1 < len(s) {
				b.WriteByte(s[j+1])
				j += 2
				continue
			}
			if d == '*' || d == '?' {
				wild = true
			}
			b.WriteByte(d)
			j++
		}
		if j == i {
			return nil, &ParseError{Pos: i, Msg: fmt.Sprintf("beklenmeyen karakter %q", string(c))}
		}
		text := b.String()
		up := strings.ToUpper(text)
		switch {
		case up == "AND":
			out = append(out, token{kind: tAnd, text: text, pos: i})
		case up == "OR":
			out = append(out, token{kind: tOr, text: text, pos: i})
		case up == "NOT":
			out = append(out, token{kind: tNot, text: text, pos: i})
		case up == "TO":
			out = append(out, token{kind: tTo, text: text, pos: i})
		default:
			out = append(out, token{kind: tTerm, text: text, pos: i, wild: wild})
		}
		i = j
	}
	out = append(out, token{kind: tEOF, pos: len(s)})
	return out, nil
}

// ── parser ───────────────────────────────────────────────────────────────

type parser struct {
	toks []token
	i    int
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) next() token { t := p.toks[p.i]; p.i++; return t }

// Parse — sorgu dizesi → AST. Boş / yalnız boşluk → (nil, nil).
func Parse(q string) (*Expr, error) {
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}
	toks, err := lex(q)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	e, err := p.parseOr("")
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, &ParseError{Pos: t.pos, Msg: "beklenmeyen " + tokDesc(t)}
	}
	return e, nil
}

func tokDesc(t token) string {
	switch t.kind {
	case tEOF:
		return "sorgu sonu"
	case tLParen:
		return "'('"
	case tRParen:
		return "')'"
	case tLBracket, tLBrace, tRBracket, tRBrace:
		return "aralık ayracı"
	case tAnd, tOr, tNot, tTo:
		return "'" + t.text + "'"
	case tField:
		return "alan '" + t.text + ":'"
	case tCmp:
		return "'" + t.text + "'"
	case tPhrase:
		return "tırnaklı ifade"
	}
	return "terim '" + t.text + "'"
}

// parseOr — field: dış `key:( … )` bağlamındaki alan; "" = serbest.
func (p *parser) parseOr(field string) (*Expr, error) {
	left, err := p.parseAnd(field)
	if err != nil {
		return nil, err
	}
	kids := []*Expr{left}
	for p.peek().kind == tOr {
		p.next()
		right, err := p.parseAnd(field)
		if err != nil {
			return nil, err
		}
		kids = append(kids, right)
	}
	if len(kids) == 1 {
		return left, nil
	}
	return &Expr{Kind: KindOr, Kids: kids}, nil
}

func (p *parser) parseAnd(field string) (*Expr, error) {
	left, err := p.parseNot(field)
	if err != nil {
		return nil, err
	}
	kids := []*Expr{left}
	for {
		t := p.peek()
		if t.kind == tAnd {
			p.next()
		} else if !startsPrimary(t) {
			break
		}
		right, err := p.parseNot(field)
		if err != nil {
			return nil, err
		}
		kids = append(kids, right)
	}
	if len(kids) == 1 {
		return left, nil
	}
	return &Expr{Kind: KindAnd, Kids: kids}, nil
}

func startsPrimary(t token) bool {
	switch t.kind {
	case tLParen, tNot, tTerm, tPhrase, tField:
		return true
	}
	return false
}

func (p *parser) parseNot(field string) (*Expr, error) {
	if p.peek().kind == tNot {
		t := p.next()
		if !startsPrimary(p.peek()) {
			return nil, &ParseError{Pos: t.pos, Msg: "'" + t.text + "' sonrasında ifade yok"}
		}
		inner, err := p.parseNot(field)
		if err != nil {
			return nil, err
		}
		if inner.Kind == KindNot { // NOT NOT x → x
			return inner.Kids[0], nil
		}
		return &Expr{Kind: KindNot, Kids: []*Expr{inner}}, nil
	}
	return p.parsePrimary(field)
}

func (p *parser) parsePrimary(field string) (*Expr, error) {
	t := p.next()
	switch t.kind {
	case tLParen:
		if p.peek().kind == tRParen {
			return nil, &ParseError{Pos: t.pos, Msg: "boş parantez"}
		}
		e, err := p.parseOr(field)
		if err != nil {
			return nil, err
		}
		if c := p.next(); c.kind != tRParen {
			return nil, &ParseError{Pos: c.pos, Msg: "')' bekleniyordu, " + tokDesc(c) + " geldi"}
		}
		return e, nil
	case tTerm:
		if field == "" && t.text == "*" {
			return nil, &ParseError{Pos: t.pos, Msg: "tek başına '*' anlamsız"}
		}
		if field != "" && t.text == "*" {
			return &Expr{Kind: KindClause, Field: field, Op: OpExists}, nil
		}
		return &Expr{Kind: KindClause, Field: field, Op: OpMatch, Value: t.text, Wildcard: t.wild}, nil
	case tPhrase:
		return &Expr{Kind: KindClause, Field: field, Op: OpMatch, Value: t.text, Phrase: true}, nil
	case tField:
		if field != "" {
			return nil, &ParseError{Pos: t.pos, Msg: "alan içinde alan: '" + t.text + ":'"}
		}
		return p.parseFieldValue(t)
	case tEOF:
		return nil, &ParseError{Pos: t.pos, Msg: "ifade bekleniyordu, sorgu bitti"}
	}
	return nil, &ParseError{Pos: t.pos, Msg: "beklenmeyen " + tokDesc(t)}
}

// parseFieldValue — `key:` sonrası: değer / tırnak / (…) / karşılaştırma /
// aralık / `*`. `_exists_:key` özel.
func (p *parser) parseFieldValue(f token) (*Expr, error) {
	name := f.text
	if strings.EqualFold(name, "_exists_") {
		v := p.next()
		if v.kind != tTerm && v.kind != tPhrase {
			return nil, &ParseError{Pos: v.pos, Msg: "_exists_: sonrasında alan adı bekleniyordu"}
		}
		return &Expr{Kind: KindClause, Field: v.text, Op: OpExists}, nil
	}
	t := p.next()
	switch t.kind {
	case tTerm:
		if t.text == "*" {
			return &Expr{Kind: KindClause, Field: name, Op: OpExists}, nil
		}
		return &Expr{Kind: KindClause, Field: name, Op: OpMatch, Value: t.text, Wildcard: t.wild}, nil
	case tPhrase:
		return &Expr{Kind: KindClause, Field: name, Op: OpMatch, Value: t.text, Phrase: true}, nil
	case tLParen:
		if p.peek().kind == tRParen {
			return nil, &ParseError{Pos: t.pos, Msg: "boş parantez"}
		}
		e, err := p.parseOr(name)
		if err != nil {
			return nil, err
		}
		if c := p.next(); c.kind != tRParen {
			return nil, &ParseError{Pos: c.pos, Msg: "')' bekleniyordu, " + tokDesc(c) + " geldi"}
		}
		return e, nil
	case tCmp:
		v := p.next()
		if v.kind != tTerm && v.kind != tPhrase {
			return nil, &ParseError{Pos: v.pos, Msg: "'" + t.text + "' sonrasında değer bekleniyordu"}
		}
		op := map[string]Op{">": OpGt, ">=": OpGte, "<": OpLt, "<=": OpLte}[t.text]
		return &Expr{Kind: KindClause, Field: name, Op: op, Value: v.text, Phrase: v.kind == tPhrase}, nil
	case tLBracket, tLBrace:
		lo := p.next()
		if lo.kind != tTerm && lo.kind != tPhrase {
			return nil, &ParseError{Pos: lo.pos, Msg: "aralık alt sınırı bekleniyordu"}
		}
		if to := p.next(); to.kind != tTo {
			return nil, &ParseError{Pos: to.pos, Msg: "'TO' bekleniyordu"}
		}
		hi := p.next()
		if hi.kind != tTerm && hi.kind != tPhrase {
			return nil, &ParseError{Pos: hi.pos, Msg: "aralık üst sınırı bekleniyordu"}
		}
		cl := p.next()
		if cl.kind != tRBracket && cl.kind != tRBrace {
			return nil, &ParseError{Pos: cl.pos, Msg: "']' ya da '}' bekleniyordu"}
		}
		e := &Expr{Kind: KindClause, Field: name, Op: OpRange,
			Lo: lo.text, Hi: hi.text, LoIncl: t.kind == tLBracket, HiIncl: cl.kind == tRBracket}
		if e.Lo == "*" {
			e.Lo = ""
		}
		if e.Hi == "*" {
			e.Hi = ""
		}
		return e, nil
	}
	return nil, &ParseError{Pos: t.pos, Msg: "'" + name + ":' sonrasında değer bekleniyordu, " + tokDesc(t) + " geldi"}
}

// ── yardımcılar ──────────────────────────────────────────────────────────

// Walk — her düğümü önce-kök sırasıyla ziyaret eder; fn false dönerse o
// dalın altına inmez.
func Walk(e *Expr, fn func(*Expr) bool) {
	if e == nil || !fn(e) {
		return
	}
	for _, k := range e.Kids {
		Walk(k, fn)
	}
}

// HasFieldClause — en az bir alan yüklemi var mı (serbest metin dışı)?
func HasFieldClause(e *Expr) bool {
	found := false
	Walk(e, func(n *Expr) bool {
		if n.Kind == KindClause && n.Field != "" {
			found = true
		}
		return !found
	})
	return found
}

// Fields — sorgudaki farklı alan adları (görülme sırasıyla).
func Fields(e *Expr) []string {
	var out []string
	seen := map[string]bool{}
	Walk(e, func(n *Expr) bool {
		if n.Kind == KindClause && n.Field != "" && !seen[n.Field] {
			seen[n.Field] = true
			out = append(out, n.Field)
		}
		return true
	})
	return out
}

// String — kanonik Lucene yazımı (testler + hata ayıklama). Parse(String(e))
// aynı ağacı verir.
func (e *Expr) String() string {
	if e == nil {
		return ""
	}
	switch e.Kind {
	case KindAnd, KindOr:
		sep := " AND "
		if e.Kind == KindOr {
			sep = " OR "
		}
		parts := make([]string, len(e.Kids))
		for i, k := range e.Kids {
			s := k.String()
			if k.Kind == KindAnd || k.Kind == KindOr {
				s = "(" + s + ")"
			}
			parts[i] = s
		}
		return strings.Join(parts, sep)
	case KindNot:
		s := e.Kids[0].String()
		if e.Kids[0].Kind != KindClause {
			s = "(" + s + ")"
		}
		return "NOT " + s
	}
	val := func(v string, phrase bool) string {
		if phrase {
			return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
		}
		return v
	}
	pre := ""
	if e.Field != "" {
		pre = e.Field + ":"
	}
	switch e.Op {
	case OpExists:
		return "_exists_:" + e.Field
	case OpGt:
		return pre + ">" + val(e.Value, e.Phrase)
	case OpGte:
		return pre + ">=" + val(e.Value, e.Phrase)
	case OpLt:
		return pre + "<" + val(e.Value, e.Phrase)
	case OpLte:
		return pre + "<=" + val(e.Value, e.Phrase)
	case OpRange:
		lo, hi := e.Lo, e.Hi
		if lo == "" {
			lo = "*"
		}
		if hi == "" {
			hi = "*"
		}
		l, r := "[", "]"
		if !e.LoIncl {
			l = "{"
		}
		if !e.HiIncl {
			r = "}"
		}
		return pre + l + lo + " TO " + hi + r
	}
	return pre + val(e.Value, e.Phrase)
}
