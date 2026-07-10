package chstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// api_tokens.go — v0.8.444 uzun ömürlü servis token'ları. Kullanım
// senaryosu: GenAI Studio gibi harici bir agent platformunun Coremetry
// MCP'sine (ve REST API'sine) login-JWT olmadan, iptal edilebilir bir
// kimlikle bağlanması. Düz token ASLA saklanmaz — yalnız sha256 hash'i;
// düz değer üretim anında BİR KEZ gösterilir (SMTP-secret sözleşmesi).
// ReplacingMergeTree(version) + revoked tombstone: iptal = yeni satır.

type APIToken struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"` // admin | editor | viewer
	Prefix    string `json:"prefix"` // cmk_ab12… (ilk 10 kr) — listede tanıma için
	TokenHash string `json:"-"`
	CreatedBy string `json:"createdBy"`
	CreatedAt int64  `json:"createdAt"` // unix ns
	Revoked   bool   `json:"revoked"`
}

const apiTokensDDL = `
CREATE TABLE IF NOT EXISTS api_tokens (
    id         String,
    name       String,
    role       LowCardinality(String),
    prefix     String,
    token_hash String,
    created_by String,
    created_at DateTime64(9),
    revoked    UInt8 DEFAULT 0,
    version    UInt64
) ENGINE = ReplacingMergeTree(version)
ORDER BY id`

// TokenPlaintextPrefix — Bearer değerinde JWT'den ayırt etme öneki.
const TokenPlaintextPrefix = "cmk_"

// HashAPIToken — düz token → saklanan hash (sha256 hex).
func HashAPIToken(plain string) string {
	h := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(h[:])
}

// NewAPITokenPlaintext — cmk_ + 32 bayt hex rastgele.
func NewAPITokenPlaintext() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return TokenPlaintextPrefix + hex.EncodeToString(b), nil
}

// CreateAPIToken — üretir, hash'ler, yazar; düz değeri döner (tek sefer).
func (s *Store) CreateAPIToken(ctx context.Context, name, role, createdBy string) (plain string, tok APIToken, err error) {
	plain, err = NewAPITokenPlaintext()
	if err != nil {
		return "", APIToken{}, err
	}
	now := time.Now()
	tok = APIToken{
		ID:        HashAPIToken(plain)[:16], // hash türevli id — çakışmasız, tahmin edilemez
		Name:      strings.TrimSpace(name),
		Role:      role,
		Prefix:    plain[:10] + "…",
		TokenHash: HashAPIToken(plain),
		CreatedBy: createdBy,
		CreatedAt: now.UnixNano(),
	}
	err = s.writeAPIToken(ctx, tok)
	return plain, tok, err
}

func (s *Store) writeAPIToken(ctx context.Context, t APIToken) error {
	batch, err := s.conn.PrepareBatch(asyncInsertCtx(ctx), `INSERT INTO api_tokens
		(id, name, role, prefix, token_hash, created_by, created_at, revoked, version)`)
	if err != nil {
		return err
	}
	rev := uint8(0)
	if t.Revoked {
		rev = 1
	}
	if err := batch.Append(t.ID, t.Name, t.Role, t.Prefix, t.TokenHash,
		t.CreatedBy, time.Unix(0, t.CreatedAt).UTC(), rev, uint64(time.Now().UnixNano())); err != nil {
		return err
	}
	return batch.Send()
}

// RevokeAPIToken — tombstone satırı (satır silinmez; audit izi kalır).
func (s *Store) RevokeAPIToken(ctx context.Context, id string) error {
	toks, err := s.ListAPITokens(ctx)
	if err != nil {
		return err
	}
	for _, t := range toks {
		if t.ID == id {
			t.Revoked = true
			return s.writeAPIToken(ctx, t)
		}
	}
	return nil
}

// ListAPITokens — hepsi (revoked dahil; UI rozetle gösterir).
func (s *Store) ListAPITokens(ctx context.Context) ([]APIToken, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT id, name, role, prefix, token_hash, created_by,
		       toUnixTimestamp64Nano(created_at), revoked
		FROM api_tokens FINAL
		ORDER BY created_at DESC
		LIMIT 500
		SETTINGS max_execution_time = 10`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		var rev uint8
		if err := rows.Scan(&t.ID, &t.Name, &t.Role, &t.Prefix, &t.TokenHash,
			&t.CreatedBy, &t.CreatedAt, &rev); err != nil {
			return nil, err
		}
		t.Revoked = rev == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

// ActiveAPITokenHashes — hash → rol haritası (auth cache'inin beslemesi).
func (s *Store) ActiveAPITokenHashes(ctx context.Context) (map[string]APIToken, error) {
	toks, err := s.ListAPITokens(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]APIToken, len(toks))
	for _, t := range toks {
		if !t.Revoked {
			out[t.TokenHash] = t
		}
	}
	return out, nil
}
