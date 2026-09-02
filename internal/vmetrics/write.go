package vmetrics

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// write.go — v0.10.292 (docs/audit/vm-metrics-migration.md Dilim 1a):
// OTLP metrik gövdesini VictoriaMetrics'e YAZAR.
//
// POST <WriteURL|BaseURL>/opentelemetry/v1/metrics — gövde protobuf
// (ExportMetricsServiceRequest), Content-Encoding gzip. promapi
// KULLANILMAZ: o okuma/JSON zarfı için; burada tel gövdesi ikili ve cevap
// gövdesiz. Ham gövde CH modelinden geri üretilmez — ingest yolu
// (otlp.Ingester.MetricForward) aldığı baytları olduğu gibi verir; dönüşüm
// kaybı sıfır (golden test yükümlülüğü otlp/forward_test.go'da).
//
// Hata sınıfları (write_test.go tablosu):
//   - 2xx (200 ve VM'in 204'ü)            → nil
//   - 400 + gövde                          → ErrRejected; VM mesajı VERBATİM
//   - 401 / 403                            → ErrUpstream, mesajda kod
//   - 408 / 429 / 5xx / bağlantı hatası    → ErrRetryable (kuyruk yeniden dener)
//   - ctx iptali                           → ctx.Err() (goroutine sızmaz)

var (
	ErrRejected  = errors.New("victoriametrics rejected the metrics body")
	ErrUpstream  = errors.New("victoriametrics upstream error")
	ErrRetryable = errors.New("victoriametrics temporarily unavailable")
	ErrNoWrite   = errors.New("victoriametrics write not configured")
)

const (
	otlpMetricsPath  = "/opentelemetry/v1/metrics"
	writeTimeout     = 10 * time.Second
	writeErrBodyMax  = 512
	writeContentType = "application/x-protobuf"
)

// WriteTarget — yazım hedefi: WriteURL doluysa o, değilse BaseURL; ikisi de
// boşsa "". Sondaki '/' kırpılır.
func WriteTarget(cfg Settings) string {
	u := strings.TrimSpace(cfg.WriteURL)
	if u == "" {
		u = strings.TrimSpace(cfg.BaseURL)
	}
	return strings.TrimRight(u, "/")
}

// WriteReady — yazım açık VE hedef var.
func (s *Service) WriteReady() bool {
	if s == nil {
		return false
	}
	cfg := s.CurrentSettings()
	return cfg.WriteEnabled && WriteTarget(cfg) != ""
}

func newWriteClient(cfg Settings) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.InsecureSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 — operatör ayarı (POC)
	}
	return &http.Client{Transport: tr, Timeout: writeTimeout}
}

// WriteOTLP — body: ExportMetricsServiceRequest protobuf'u; gzipped: gövde
// zaten gzip'li mi (OTLP/HTTP alıcısı sıkıştırılmış geldiyse tekrar
// açıp sıkıştırmaz — bayt bayt ilet). Sıkıştırılmamış gövde burada gzip'lenir.
func (s *Service) WriteOTLP(ctx context.Context, body []byte, gzipped bool) error {
	if s == nil {
		return ErrNoWrite
	}
	s.mu.RLock()
	cfg, cli, tok := s.cfg, s.writeHTTP, effectiveToken(s.cfg, s.resolvedToken)
	s.mu.RUnlock()
	if !cfg.WriteEnabled || WriteTarget(cfg) == "" {
		return ErrNoWrite
	}
	if cli == nil {
		cli = newWriteClient(cfg)
	}
	return writeOTLPWith(ctx, cli, cfg, tok, body, gzipped)
}

func writeOTLPWith(ctx context.Context, cli *http.Client, cfg Settings, token string, body []byte, gzipped bool) error {
	target := WriteTarget(cfg)
	if target == "" {
		return ErrNoWrite
	}
	payload := body
	if !gzipped {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(body); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
		payload = buf.Bytes()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target+otlpMetricsPath, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writeContentType)
	req.Header.Set("Content-Encoding", "gzip")
	if strings.EqualFold(cfg.AuthType, "bearer") && token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := cli.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", ErrRetryable, err)
	}
	defer res.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(res.Body, writeErrBodyMax))
	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		return nil
	case res.StatusCode == http.StatusBadRequest:
		return fmt.Errorf("%w: %s", ErrRejected, strings.TrimSpace(string(msg)))
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: HTTP %d", ErrUpstream, res.StatusCode)
	case res.StatusCode == http.StatusRequestTimeout || res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500:
		return fmt.Errorf("%w: HTTP %d", ErrRetryable, res.StatusCode)
	}
	return fmt.Errorf("%w: HTTP %d %s", ErrUpstream, res.StatusCode, strings.TrimSpace(string(msg)))
}
