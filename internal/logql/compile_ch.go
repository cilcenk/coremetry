package logql

import (
	"strconv"
	"strings"
)

// compile_ch.go — AST → ClickHouse WHERE yüklemi (v0.10.279).
//
// Paket SQL şemasını BİLMEZ; alan → ifade eşlemesini Target verir (chstore
// uygular: kolonlar, res-array zincirleri, attr lookup). Bu ayrım
// logstore ↔ chstore import yönünü korur ve derleyiciyi sahte bir Target ile
// saf test edilebilir kılar.
//
// Bağlama sırası: her `?` çıktıdaki sırayla args'a eklenir; FieldRef.Expr
// içindeki `?`'ler (attr lookup'ın iki alan adı) ifadeden ÖNCE gelir.

// FieldKind — çözümlenen alanın karşılaştırma semantiği.
type FieldKind int

const (
	// FieldBody — log gövdesi: alt-dize harf-duyarsız
	// (multiSearchAnyCaseInsensitive), joker → ilike.
	FieldBody FieldKind = iota
	// FieldString — String kolon/ifade: tam eşitlik (ES keyword semantiği).
	FieldString
	// FieldFold — String ama harf-duyarsız eşitlik (severity: `level:error`
	// ile `ERROR` aynı — v0.5.201 sınıfı).
	FieldFold
	// FieldNumeric — sayısal kolon: karşılaştırmalar sayısal.
	FieldNumeric
	// FieldID — trace_id/span_id: değer küçük harfe indirilir.
	FieldID
)

// FieldRef — bir alanın SQL bağlaması.
type FieldRef struct {
	Kind FieldKind
	Expr string // `?` içerebilir → Args
	Args []any
	// ExistsExpr — varlık yüklemi; boşsa `Expr != ''` kullanılır.
	ExistsExpr string
	ExistsArgs []any
}

// Target — derleyicinin şema bilgisi.
type Target interface {
	Resolve(field string) FieldRef
	// IDColumns — serbest metin terimi çıplak bir id ise, gövde aramasına OR
	// ile eklenecek eşitlik kolonları (trace_id, span_id); değilse nil.
	IDColumns(term string) []string
}

type chBuilder struct {
	t    Target
	args []any
}

// Compile — nil ifade → ("", nil). Dönen SQL parantezli tek yüklemdir,
// WHERE'e `AND` ile eklenebilir.
func Compile(e *Expr, t Target) (string, []any) {
	if e == nil {
		return "", nil
	}
	b := &chBuilder{t: t}
	sql := b.expr(e)
	return sql, b.args
}

func (b *chBuilder) expr(e *Expr) string {
	switch e.Kind {
	case KindAnd, KindOr:
		sep := " AND "
		if e.Kind == KindOr {
			sep = " OR "
		}
		parts := make([]string, len(e.Kids))
		for i, k := range e.Kids {
			parts[i] = b.expr(k)
		}
		return "(" + strings.Join(parts, sep) + ")"
	case KindNot:
		return "NOT " + b.expr(e.Kids[0])
	}
	return b.clause(e)
}

func (b *chBuilder) clause(e *Expr) string {
	if e.Field == "" {
		return b.freeText(e)
	}
	ref := b.t.Resolve(e.Field)
	switch e.Op {
	case OpExists:
		if ref.ExistsExpr != "" { // hedef parantezli verir
			b.args = append(b.args, ref.ExistsArgs...)
			return ref.ExistsExpr
		}
		b.args = append(b.args, ref.Args...)
		return "(" + ref.Expr + " != '')"
	case OpMatch:
		return b.match(ref, e.Value, e.Phrase, e.Wildcard)
	case OpGt, OpGte, OpLt, OpLte:
		return b.compare(ref, opSQL(e.Op), e.Value)
	case OpRange:
		var parts []string
		if e.Lo != "" {
			op := ">"
			if e.LoIncl {
				op = ">="
			}
			parts = append(parts, b.compare(ref, op, e.Lo))
		}
		if e.Hi != "" {
			op := "<"
			if e.HiIncl {
				op = "<="
			}
			parts = append(parts, b.compare(ref, op, e.Hi))
		}
		if len(parts) == 0 {
			b.args = append(b.args, ref.Args...)
			return "(" + ref.Expr + " != '')"
		}
		return "(" + strings.Join(parts, " AND ") + ")"
	}
	return "1"
}

func opSQL(op Op) string {
	switch op {
	case OpGt:
		return ">"
	case OpGte:
		return ">="
	case OpLt:
		return "<"
	}
	return "<="
}

// freeText — alan olmayan terim: gövde araması. Çıplak hex id'de kolon
// dalları (v0.8.521 sözleşmesi, IDColumns üzerinden — hex tanımı tek yerde).
func (b *chBuilder) freeText(e *Expr) string {
	if e.Wildcard && !e.Phrase {
		b.args = append(b.args, likePattern(e.Value))
		return "ilike(body, ?)"
	}
	b.args = append(b.args, e.Value)
	base := "multiSearchAnyCaseInsensitive(body, [?])"
	cols := b.t.IDColumns(e.Value)
	if len(cols) == 0 || e.Phrase {
		return base
	}
	id := strings.ToLower(strings.TrimSpace(e.Value))
	parts := []string{base}
	for _, c := range cols {
		b.args = append(b.args, id)
		parts = append(parts, c+" = ?")
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

func (b *chBuilder) match(ref FieldRef, val string, phrase, wild bool) string {
	wild = wild && !phrase
	switch ref.Kind {
	case FieldBody:
		if wild {
			b.args = append(b.args, ref.Args...)
			b.args = append(b.args, likePattern(val))
			return "ilike(" + ref.Expr + ", ?)"
		}
		b.args = append(b.args, ref.Args...)
		b.args = append(b.args, val)
		return "multiSearchAnyCaseInsensitive(" + ref.Expr + ", [?])"
	case FieldFold:
		b.args = append(b.args, ref.Args...)
		if wild {
			b.args = append(b.args, likePattern(val))
			return "ilike(" + ref.Expr + ", ?)"
		}
		b.args = append(b.args, val)
		return "(lower(" + ref.Expr + ") = lower(?))"
	case FieldID:
		b.args = append(b.args, ref.Args...)
		if wild {
			b.args = append(b.args, likePattern(strings.ToLower(val)))
			return "like(" + ref.Expr + ", ?)"
		}
		b.args = append(b.args, strings.ToLower(strings.TrimSpace(val)))
		return "(" + ref.Expr + " = ?)"
	case FieldNumeric:
		b.args = append(b.args, ref.Args...)
		if n, ok := parseNum(val); ok && !wild {
			b.args = append(b.args, n)
			return "(" + ref.Expr + " = ?)"
		}
		if wild {
			b.args = append(b.args, likePattern(val))
			return "like(toString(" + ref.Expr + "), ?)"
		}
		b.args = append(b.args, val)
		return "(toString(" + ref.Expr + ") = ?)"
	}
	// FieldString
	b.args = append(b.args, ref.Args...)
	if wild {
		b.args = append(b.args, likePattern(val))
		return "like(" + ref.Expr + ", ?)"
	}
	b.args = append(b.args, val)
	return "(" + ref.Expr + " = ?)"
}

// compare — sayısal değerde sayısal karşılaştırma (String alanlarda
// toFloat64OrNull ile; sayı olmayan satırlar NULL → yüklem dışı), aksi
// hâlde sözlüksel.
func (b *chBuilder) compare(ref FieldRef, op, val string) string {
	b.args = append(b.args, ref.Args...)
	n, isNum := parseNum(val)
	switch ref.Kind {
	case FieldNumeric:
		if isNum {
			b.args = append(b.args, n)
			return "(" + ref.Expr + " " + op + " ?)"
		}
		b.args = append(b.args, val)
		return "(toString(" + ref.Expr + ") " + op + " ?)"
	case FieldString, FieldFold:
		if isNum {
			b.args = append(b.args, n)
			return "(toFloat64OrNull(" + ref.Expr + ") " + op + " ?)"
		}
	}
	b.args = append(b.args, val)
	return "(" + ref.Expr + " " + op + " ?)"
}

func parseNum(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f, err == nil
}

// likePattern — Lucene joker → LIKE deseni. Desendeki gerçek `%`/`_`
// kaçışlanır (CH LIKE kaçış karakteri `\`), `*` → `%`, `?` → `_`.
func likePattern(v string) string {
	var b strings.Builder
	for _, r := range v {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '%':
			b.WriteString(`\%`)
		case '_':
			b.WriteString(`\_`)
		case '*':
			b.WriteByte('%')
		case '?':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
