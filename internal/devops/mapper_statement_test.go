package devops

import (
	"context"
	"strings"
	"testing"

	"github.com/cilcenk/coremetry/internal/stackparse"
)

// mapper_statement_test.go — v0.10.113. STATEMENT ID → SORGU BLOĞU.
//
// v0.10.73 mapper XML'ini buluyordu ama dosyanın İLK 200 satırını
// gönderiyordu; statement 400. satırdaysa model namespace/resultMap
// görüyor, sorguyu görmüyordu — ve pencere listesinin sonunda olduğu
// için 4000 rune kırpması önce onu düşürüyordu. Operatör spec'i:
// "mapper/XML statement id'si hatadan çıkarılabiliyorsa ilgili sorgu
// bloğunu bağlama ekle".

const mapperXML = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE mapper PUBLIC "-//mybatis.org//DTD Mapper 3.0//EN" "http://mybatis.org/dtd/mybatis-3-mapper.dtd">
<mapper namespace="com.acme.fraud.IntTfraudMapper">
  <resultMap id="ariCTelefonSelect" type="com.acme.fraud.Telefon">
    <result column="TELNO" property="telNo"/>
  </resultMap>
  <sql id="cols">TELNO, MUSTERI_NO</sql>
  <select id="baskaSelect" resultMap="ariCTelefonSelect">
    SELECT <include refid="cols"/> FROM TFRAUD WHERE X = #{x}
  </select>
  <select id="ariCTelefonSelect" resultMap="ariCTelefonSelect">
    SELECT <include refid="cols"/>
      FROM INT_TFRAUD
     WHERE MUSTERI_NO = #{musteriNo}
       AND TELNO = <![CDATA[ #{telNo} ]]>
  </select>
  <insert id="ariCTelefonInsert">
    INSERT INTO INT_TFRAUD (MUSTERI_NO, TELNO) VALUES (#{musteriNo}, #{telNo})
  </insert>
</mapper>
`

func TestMapperStatementWindowTable(t *testing.T) {
	cases := []struct {
		name     string
		id       string
		wantFrom int
		wantTo   int
		wantHas  string
		wantNone bool
	}{
		{"select bloğu, gerçek satır numaralarıyla", "ariCTelefonSelect", 11, 16, "FROM INT_TFRAUD", false},
		{"resultMap aynı id'yi taşıyor ama STATEMENT önce", "ariCTelefonSelect", 11, 16, "<select id=\"ariCTelefonSelect\"", false},
		{"insert bloğu", "ariCTelefonInsert", 17, 19, "INSERT INTO INT_TFRAUD", false},
		{"sql fragment", "cols", 7, 7, "<sql id=\"cols\">", false},
		{"id yok → boş", "yokBoyleBirsey", 0, 0, "", true},
		{"boş id → boş", "", 0, 0, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := MapperStatementWindow(mapperXML, c.id, 80)
			if c.wantNone {
				if w.Content != "" {
					t.Fatalf("boş beklenirdi: %+v", w)
				}
				return
			}
			if w.FromLine != c.wantFrom || w.ToLine != c.wantTo {
				t.Fatalf("aralık %d-%d, istenen %d-%d:\n%s", w.FromLine, w.ToLine, c.wantFrom, c.wantTo, w.Content)
			}
			if !strings.Contains(w.Content, c.wantHas) {
				t.Fatalf("blokta %q yok:\n%s", c.wantHas, w.Content)
			}
			if !w.Resource || w.Frame != "statement id: "+c.id {
				t.Errorf("kaynak damgası: resource=%v frame=%q", w.Resource, w.Frame)
			}
			// Satır numarası ÖNEKLİ: bütçe kesicisi ve lineNumberOf aynı biçimi okur.
			if !strings.HasPrefix(w.Content, itoaLine(c.wantFrom)+"| ") {
				t.Errorf("satır öneki yok: %q", w.Content[:20])
			}
		})
	}
}

func itoaLine(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// TestMapperStatementWindowCap — dev bir statement tavanda kesilir; kesim
// SÖYLENİR (Frame'e eklenir) ki model bloğun tamamını gördüğünü sanmasın.
func TestMapperStatementWindowCap(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("<mapper>\n<select id=\"big\">\n")
	for i := 0; i < 200; i++ {
		sb.WriteString("  SELECT_LINE\n")
	}
	sb.WriteString("</select>\n</mapper>\n")
	w := MapperStatementWindow(sb.String(), "big", 40)
	if w.FromLine != 2 || w.ToLine != 41 {
		t.Fatalf("tavan uygulanmadı: %d-%d", w.FromLine, w.ToLine)
	}
	if !strings.Contains(w.Frame, "kırpıldı") {
		t.Errorf("kesim söylenmedi: %q", w.Frame)
	}
	// Kapanış etiketi olmayan bozuk XML: bloğun sonu tavana kadar.
	broken := "<mapper>\n<select id=\"x\">\n SELECT 1\n"
	if w := MapperStatementWindow(broken, "x", 80); w.FromLine != 2 || w.ToLine != 3 {
		t.Errorf("kapanışsız blok: %d-%d", w.FromLine, w.ToLine)
	}
}

// TestHuntResourcesPrefersStatementBlock — Member taşıyan aday için
// önce statement bloğu; Member yok ya da bulunamadıysa eski ilk-200
// davranışı (v0.10.73) aynen.
func TestHuntResourcesPrefersStatementBlock(t *testing.T) {
	paths := []string{"/src/main/resources/mapper/IntTfraudMapper.xml", "/src/main/resources/mapper/Other.xml"}
	fetch := func(_ context.Context, p string) (string, error) {
		if strings.HasSuffix(p, "IntTfraudMapper.xml") {
			return mapperXML, nil
		}
		return "<mapper>\n<select id=\"q\">SELECT 1</select>\n</mapper>\n", nil
	}
	refs := []stackparse.ResourceRef{
		{Base: "IntTfraudMapper", Member: "ariCTelefonSelect"},
		{Base: "Other"},
	}
	ws := huntResources(context.Background(), refs, paths, fetch)
	if len(ws) != 2 {
		t.Fatalf("pencere=%d, 2 bekleniyordu: %+v", len(ws), ws)
	}
	if ws[0].FromLine != 11 || !strings.Contains(ws[0].Content, "FROM INT_TFRAUD") || ws[0].Frame != "statement id: ariCTelefonSelect" {
		t.Errorf("statement bloğu seçilmedi: %+v", ws[0])
	}
	if ws[1].FromLine != 1 || ws[1].Frame != "" {
		t.Errorf("Member'sız aday ilk-N davranışını kaybetti: %+v", ws[1])
	}
	// Member var ama XML'de yok → ilk-N'e düşer (fail-open).
	refs = []stackparse.ResourceRef{{Base: "IntTfraudMapper", Member: "olmayanId"}}
	ws = huntResources(context.Background(), refs, paths, fetch)
	if len(ws) != 1 || ws[0].FromLine != 1 {
		t.Errorf("bulunamayan id ilk-N'e düşmedi: %+v", ws)
	}
	// Prompt etiketi: statement penceresi satır aralığı ve id ile sunulur.
	block := (CodeContext{Repo: "r", Windows: []CodeWindow{ws[0], {Path: "/m.xml", Resource: true, FromLine: 11, ToLine: 16, Frame: "statement id: ariCTelefonSelect", Content: "11| x"}}}).PromptBlock()
	if !strings.Contains(block, "(satır 11-16, statement id: ariCTelefonSelect)") {
		t.Errorf("prompt etiketi: %s", block)
	}
}
