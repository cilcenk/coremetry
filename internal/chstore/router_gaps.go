package chstore

import (
	"context"
	"time"
)

// Router boşluk raporu — v0.9.549.
//
// CoSRE'nin guided router'ı 16 intent tanıyor; tanımayan soru serbest
// tool döngüsüne düşüyor. O döngü çalışır ama pahalıdır (N tur LLM
// çağrısı) ve küçük modelde kalitesi düşüktür — asıl mesele şu:
// HANGİ soruların düştüğünü kimse görmüyordu, dolayısıyla "sıradaki
// intent ne olmalı" sorusu sezgiyle cevaplanıyordu.
//
// Yeni kayıt GEREKMEDİ: serbest döngü zaten ai_calls'a surface='chat'
// ile yazıyor ve prompt_sample tam olarak KULLANICININ SORUSU
// (copilot_chat.go: RecordUsage(..., lastUserText(req.Messages), ...)).
// Guided yol surface='chat-guided' yazıyor, yani ayrım hazır duruyordu.
//
// Rapor bu yüzden saf bir OKUMA — ekstra maliyet yok, geçmiş veri de
// dahil.

type RouterGap struct {
	// Question — operatörün yazdığı soru (prompt_sample, kırpılmış).
	Question string `json:"question"`
	Count    uint64 `json:"count"`
	LastAt   int64  `json:"lastAt"` // unix ns
	// Users — kaç FARKLI kullanıcı sordu. Tek kişinin ısrarla denediği
	// bir soru ile ekibin tamamının sorduğu soru aynı öncelikte
	// değildir; sayı tek başına bunu ayırt edemez.
	Users uint64 `json:"users"`
}

// RouterGaps — guided router'ın yakalayamadığı sorular, sıklığa göre.
//
// surface='chat' = serbest tool döngüsü (guided 'chat-guided' yazar).
// Boş prompt_sample elenir: kayıt var ama soru yok demek, sıralamada
// gürültü.
// v0.10.172 — 'chat-intent-none': niyet sınıflandırıcısı none dediği sorular
// (on_no_loop kipinde serbest döngü koşmaz, 'chat' satırı yazılmaz — rapor
// kör kalmasın; copilot_intent.go).
func (s *Store) RouterGaps(ctx context.Context, since time.Duration, limit int) ([]RouterGap, error) {
	if since <= 0 {
		since = 7 * 24 * time.Hour
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.conn.Query(ctx, `
		SELECT prompt_sample                        AS q,
		       count()                              AS n,
		       toUnixTimestamp64Nano(max(created_at)) AS last_at,
		       uniqExact(user_id)                   AS users
		FROM ai_calls
		WHERE surface IN ('chat', 'chat-intent-none')
		  AND created_at >= ?
		  AND prompt_sample != ''
		GROUP BY q
		ORDER BY n DESC, last_at DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`,
		chDateTime64Arg(time.Now().Add(-since)), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RouterGap, 0, limit)
	for rows.Next() {
		var g RouterGap
		if err := rows.Scan(&g.Question, &g.Count, &g.LastAt, &g.Users); err != nil {
			continue
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
