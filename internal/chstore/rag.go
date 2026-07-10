package chstore

import (
	"context"
	"fmt"
	"time"
)

// rag.go — v0.8.438 RAG (doküman soru-cevap) depo katmanı.
//
// Tek tablo: rag_chunks. saved_views'a BİLEREK gitmiyor (invariant #5
// istisnası, savunma): embedding Array(Float32) + cosineDistance
// sorgusu kendi şeklini ister ve bu İÇERİK verisidir, kullanıcı view
// state'i değil. ReplacingMergeTree(version) ORDER BY (doc_id,
// chunk_idx): bir dokümanın yeniden yüklenmesi / wiki sayfasının
// yeniden senkronu aynı (doc_id, chunk_idx) satırlarını yeni
// version'la değiştirir — diff mantığı bedava. Hacim düşük (yüzlerce
// doküman × onlarca chunk), FINAL okumaları bütçe içinde.
type RagChunk struct {
	// SourceHash (v0.8.442) — url kaynağında sayfa içeriğinin sha256'sı;
	// senkron diff'i "hash değişmediyse yeniden embed etme" bununla yapar.
	SourceHash string `json:"-"`
	DocID      string            `json:"docId"`
	DocName    string            `json:"docName"`
	Source     string            `json:"source"` // upload | url
	SourceRef  string            `json:"sourceRef,omitempty"` // url kaynağında sayfa adresi
	UploadedBy string            `json:"uploadedBy,omitempty"`
	ChunkIdx   uint32            `json:"chunkIdx"`
	Content    string            `json:"content"`
	Embedding  []float32         `json:"-"`
}

// RagDocument — liste görünümü (GROUP BY projeksiyonu).
type RagDocument struct {
	DocID      string `json:"docId"`
	DocName    string `json:"docName"`
	Source     string `json:"source"`
	SourceRef  string `json:"sourceRef,omitempty"`
	UploadedBy string `json:"uploadedBy,omitempty"`
	Chunks     uint64 `json:"chunks"`
	Bytes      uint64 `json:"bytes"`
	UpdatedAt  int64  `json:"updatedAt"` // unix ns
	SourceHash string `json:"-"`
}

// RagHit — retrieval sonucu: chunk + benzerlik skoru.
type RagHit struct {
	RagChunk
	Score float64 `json:"score"` // 1 - cosineDistance; 1.0 = özdeş
}

const ragChunksDDL = `
CREATE TABLE IF NOT EXISTS rag_chunks (
    doc_id      String,
    doc_name    String,
    source      LowCardinality(String),
    source_ref  String DEFAULT '',
    uploaded_by String DEFAULT '',
    chunk_idx   UInt32,
    content     String CODEC(ZSTD(3)),
    embedding   Array(Float32),
    source_hash String DEFAULT '',
    updated_at  DateTime64(9) DEFAULT now64(9),
    version     UInt64
) ENGINE = ReplacingMergeTree(version)
ORDER BY (doc_id, chunk_idx)`

// ReplaceDocumentChunks — bir dokümanın TÜM chunk'larını tek version
// damgasıyla yazar. Eski yüklemenin daha uzun kuyruk chunk'ları (yeni
// içerik kısaldıysa) ayrıca temizlenir.
func (s *Store) ReplaceDocumentChunks(ctx context.Context, chunks []RagChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	version := uint64(time.Now().UnixNano())
	ctx2 := asyncInsertCtx(ctx)
	batch, err := s.conn.PrepareBatch(ctx2, `INSERT INTO rag_chunks
		(doc_id, doc_name, source, source_ref, uploaded_by, chunk_idx, content, embedding, source_hash, updated_at, version)`)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, c := range chunks {
		if err := batch.Append(c.DocID, c.DocName, c.Source, c.SourceRef, c.UploadedBy,
			c.ChunkIdx, c.Content, c.Embedding, c.SourceHash, now.UTC(), version); err != nil {
			return err
		}
	}
	if err := batch.Send(); err != nil {
		return err
	}
	// Kuyruk temizliği: yeni chunk sayısının ötesindeki eski satırlar.
	// Düşük hacimli state tablosunda hafif mutation kabul edilebilir
	// (ALTER DELETE burada nadir ve sınırlı — upload/senkron anı).
	return s.conn.Exec(ctx, `ALTER TABLE `+s.mutationTarget("rag_chunks")+` DELETE
		WHERE doc_id = ? AND chunk_idx >= ?`,
		chunks[0].DocID, uint32(len(chunks)))
}

// DeleteRagDocument — dokümanı tamamen kaldırır (admin, audit'li).
func (s *Store) DeleteRagDocument(ctx context.Context, docID string) error {
	return s.conn.Exec(ctx, `ALTER TABLE `+s.mutationTarget("rag_chunks")+` DELETE WHERE doc_id = ?`, docID)
}

// ListRagDocuments — katalog: doküman başına chunk sayısı + boyut.
func (s *Store) ListRagDocuments(ctx context.Context) ([]RagDocument, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT doc_id, anyLast(doc_name), anyLast(source), anyLast(source_ref),
		       anyLast(uploaded_by), count() AS chunks, sum(length(content)) AS bytes,
		       toUnixTimestamp64Nano(max(updated_at)), anyLast(source_hash)
		FROM rag_chunks FINAL
		GROUP BY doc_id
		ORDER BY doc_id
		LIMIT 1000
		SETTINGS max_execution_time = 10`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RagDocument
	for rows.Next() {
		var d RagDocument
		if err := rows.Scan(&d.DocID, &d.DocName, &d.Source, &d.SourceRef,
			&d.UploadedBy, &d.Chunks, &d.Bytes, &d.UpdatedAt, &d.SourceHash); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ragTopKSQL — retrieval: soru embedding'ine kosinüs benzerliğiyle en
// yakın k chunk. Düşük hacimde brute-force tarama bütçe içinde
// (~100k chunk'a kadar); üzeri ANN index follow-up'ı.
const ragTopKSQL = `
		SELECT doc_id, doc_name, source, source_ref, chunk_idx, content,
		       1 - cosineDistance(embedding, ?) AS score
		FROM rag_chunks FINAL
		WHERE length(embedding) = length(?)
		ORDER BY score DESC
		LIMIT ?
		SETTINGS max_execution_time = 10`

// TopKRagChunks — en benzer k chunk (k [1,20] aralığına kelepçelenir).
func (s *Store) TopKRagChunks(ctx context.Context, queryEmbedding []float32, k int) ([]RagHit, error) {
	if len(queryEmbedding) == 0 {
		return nil, fmt.Errorf("empty query embedding")
	}
	if k < 1 {
		k = 5
	}
	if k > 20 {
		k = 20
	}
	rows, err := s.conn.Query(ctx, ragTopKSQL, queryEmbedding, queryEmbedding, k)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RagHit
	for rows.Next() {
		var h RagHit
		if err := rows.Scan(&h.DocID, &h.DocName, &h.Source, &h.SourceRef,
			&h.ChunkIdx, &h.Content, &h.Score); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// mutationTarget — cluster modunda mutation'lar Distributed sarmalayıcı
// yerine _local tabloya (ON CLUSTER) gitmeli; tek düğümde tablo adının
// kendisi. (dropCombinedMV'nin bilinen dersinin mutation hali.)
func (s *Store) mutationTarget(table string) string {
	if s.clusterMode() {
		return table + "_local ON CLUSTER " + s.ClusterName()
	}
	return table
}
