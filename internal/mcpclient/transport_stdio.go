package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// transport_stdio.go — STDIO taşıması: sunucu bir alt süreçtir,
// mesajlar satır-ayrımlı JSON olarak stdin/stdout üzerinden akar.
//
// ⚠ OpenShift'te alt süreç başlatmak kısıtlı olabilir; taşıma gemide
// ama VARSAYILAN kullanım HTTP (plan riski). Komut yolu YALNIZ operatör
// ayarından gelir — bu paket kendi başına hiçbir komut seçmez.
//
// Süreç ömrü Call ctx'ine BAĞLANMAZ (uzun ömürlü sunucu, istek başına
// değil); indirme Close'dadır: stdin kapat → kısa bekle → öldür.
//
// Okuyucu tek goroutine: yanıtları bekleyen çağrılara kimlikle
// dağıtır, bildirimleri kanala akıtır. Panik guard'ı var — api
// katmanında recover middleware YOK (keşif bulgusu), kopuk goroutine'de
// panik süreci öldürür.

// stdioLineCap — tek stdout satırının azami boyutu. HTTP tarafındaki
// httpBodyCap ile aynı gerekçe: boyutu bilinmeyen dış süreç belleği
// dolduramaz.
const stdioLineCap = 1 << 20 // 1 MiB

// stdioStopGrace — Close'da stdin kapandıktan sonra sürece tanınan süre.
const stdioStopGrace = 3 * time.Second

type stdioTransport struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser

	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[int64]chan rpcEnvelope
	closed  bool

	seq   atomic.Int64
	notif chan string
	done  chan struct{}
}

// newStdioTransport — süreci başlatır ve okuyucuyu kurar. Hata,
// komutun hiç başlayamadığı hâldir; sonradan ölen süreci Call'lar
// "süreç kapandı" hatasıyla görür.
func newStdioTransport(cfg ServerConfig) (*stdioTransport, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, fmt.Errorf("stdio sunucusu %q: komut boş", cfg.Name)
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// stderr tanı için KAPALI tavanla okunur; sunucular oraya log basar
	// ve okunmazsa pipe dolup süreci kilitleyebilir.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("stdio sunucusu %q başlatılamadı: %w", cfg.Name, err)
	}
	t := &stdioTransport{
		cmd: cmd, stdin: stdin,
		pending: map[int64]chan rpcEnvelope{},
		notif:   make(chan string, 8),
		done:    make(chan struct{}),
	}
	go t.readLoop(cfg.Name, stdout)
	go func() {
		// Boşalt ve at: tavan kadar oku, gerisini yut.
		_, _ = io.Copy(io.Discard, io.LimitReader(stderr, stdioLineCap))
		_, _ = io.Copy(io.Discard, stderr)
	}()
	return t, nil
}

func (t *stdioTransport) Notifications() <-chan string { return t.notif }

func (t *stdioTransport) readLoop(name string, stdout io.Reader) {
	// Panik guard'ı: kopuk goroutine'de panik SÜRECİ öldürür ve api
	// katmanında recover middleware yok (api.go:11085 emsali).
	defer func() {
		if r := recover(); r != nil {
			log.Printf("mcpclient: %s okuyucusunda panik: %v", name, r)
		}
		t.failAll(fmt.Errorf("mcp stdio sunucusu %q kapandı", name))
		close(t.done)
	}()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), stdioLineCap)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var env rpcEnvelope
		if json.Unmarshal([]byte(line), &env) != nil {
			continue // gürültü satırı taşımayı düşürmez
		}
		if env.ID == nil {
			if env.Method != "" {
				select {
				case t.notif <- env.Method:
				default:
				}
			}
			continue
		}
		t.mu.Lock()
		ch := t.pending[*env.ID]
		delete(t.pending, *env.ID)
		t.mu.Unlock()
		if ch != nil {
			ch <- env
		}
	}
}

// failAll — bekleyen tüm çağrılara süreç-kapandı hatasını dağıtır.
func (t *stdioTransport) failAll(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	for id, ch := range t.pending {
		delete(t.pending, id)
		ch <- rpcEnvelope{Error: &rpcError{Code: -32000, Message: err.Error()}}
	}
}

func (t *stdioTransport) send(env rpcEnvelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_, err = t.stdin.Write(append(body, '\n'))
	return err
}

func (t *stdioTransport) Notify(_ context.Context, method string, params any) error {
	return t.send(rpcEnvelope{JSONRPC: "2.0", Method: method, Params: params})
}

func (t *stdioTransport) Call(ctx context.Context, method string, params, result any) error {
	id := t.seq.Add(1)
	ch := make(chan rpcEnvelope, 1)
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return fmt.Errorf("mcp stdio taşıması kapalı")
	}
	t.pending[id] = ch
	t.mu.Unlock()

	if err := t.send(rpcEnvelope{JSONRPC: "2.0", ID: &id, Method: method, Params: params}); err != nil {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return fmt.Errorf("mcp %s yazılamadı: %w", method, err)
	}
	select {
	case env := <-ch:
		if env.Error != nil {
			return env.Error
		}
		if result != nil && len(env.Result) > 0 {
			if err := json.Unmarshal(env.Result, result); err != nil {
				return fmt.Errorf("mcp %s: yanıt çözümlenemedi: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return ctx.Err()
	}
}

// Close — nazik iniş: stdin kapanır (sunucular EOF'ta çıkar), süre
// tanınır, çıkmazsa öldürülür. İkinci çağrı zararsız.
func (t *stdioTransport) Close() error {
	_ = t.stdin.Close()
	select {
	case <-t.done:
	case <-time.After(stdioStopGrace):
		_ = t.cmd.Process.Kill()
		<-t.done
	}
	return t.cmd.Wait()
}
