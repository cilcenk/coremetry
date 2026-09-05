package mcp

// observe.go — v0.10.426 (CoSRE denetimi M1): GELEN MCP yürütmelerinin
// gözlem kancası. Giden çağrılar (mcpclient) hem span'lı hem audit'liydi;
// gelen tools/call / resources/read / prompts/get hiçbir iz bırakmıyordu
// (yalnız otelhttp'nin "POST /api/mcp" span'ı — araç adı yok, süre yok).
//
// Paket auth-agnostik ve coremetry-import'suz kalır (toolerr_test
// TestMCPPackageHasNoCoremetryImports): span + audit api katmanında
// (mcp_observe.go), buraya yalnız CallGate biçiminde bir fonksiyon iner.
// Begin/end şekli BİLİNÇLİ — sonradan çağrılan bir Observe(dur, err) yaprak
// span üretirdi ve handler'ın clickhouse.query span'ları HTTP span'ına
// asılı kalırdı; dönen ctx handler'a verilir, çocuklar iç içe geçer.
//
// Kapıdan SONRA, handler'dan ÖNCE: bilinmeyen araç ve kapı reddi
// gözlemlenmez (çalışmayan çağrı kanıt değildir — runGate düzeni).

import (
	"context"
	"encoding/json"
)

// CallOutcome — yürütmenin sonucu. ErrorClass toolerr sınıflarından
// (timeout/cancelled/…), ÜÇ türde de dolu (v0.10.430 — eskiden yalnız
// tool çıkışı sınıf taşıyordu, kaynak/prompt hatası error_class="" ile
// düşüyordu); ResultBytes başarılı sonucun ham boyu — tool'da JSON gövde,
// kaynakta metin, prompt'ta mesaj metinlerinin toplamı (v0.10.430 —
// eskiden MESAJ SAYISI yazılıyordu, bayt adlı attribute 2 okuyordu).
type CallOutcome struct {
	Err         error
	ErrorClass  string
	ResultBytes int
}

// outcomeErrorClass — nil hata boş sınıf; aksi toolerr sınıfı. Tek yerden:
// dört çıkış da aynı sınıflandırıcıdan geçer (v0.10.430).
func outcomeErrorClass(err error) string {
	if err == nil {
		return ""
	}
	return classifyToolErrorClass(err)
}

// promptMessagesBytes — prompts/get sonucunun ham boyu: mesaj metinlerinin
// bayt toplamı (rol/tip zarfı sayılmaz — tool'daki JSON gövdesiyle aynı
// mertebede, "hangi ajan bütçeyi yaktı" sorusu için yeterli).
func promptMessagesBytes(msgs []PromptMessage) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content.Text)
	}
	return n
}

// Observer — kind "tool" | "resource" | "prompt"; name araç adı / URI /
// prompt adı; args yalnız tool'da (ham JSON, önizleme için).
type Observer func(ctx context.Context, kind, name string, args json.RawMessage) (context.Context, func(CallOutcome))

// SetObserver — boot'ta bir kez (api.SetMCP), sonra değişmez (kilitsiz okunur).
func (s *Server) SetObserver(o Observer) {
	s.observe = o
}

// beginObserve — nil-safe; dört yürütme yolu da bunu çağırır (yeni yol
// kancayı unutamasın — runGate ilkesi).
func (s *Server) beginObserve(ctx context.Context, kind, name string, args json.RawMessage) (context.Context, func(CallOutcome)) {
	if s.observe == nil {
		return ctx, func(CallOutcome) {}
	}
	return s.observe(ctx, kind, name, args)
}
