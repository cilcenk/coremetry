---
name: api-route
description: Add a new HTTP endpoint to Coremetry — ALWAYS in its own internal/api/<domain>.go file with a `registerXxxRoutes(mux)` method, so api.go grows by exactly one line (or by zero — register via route_registry.go init()). Covers route/auth/audit/serveCached/writeErr conventions, the four frontend touchpoints, and the silent-failure list (an unregistered route answers HTTP 200 with a blank page, never 404). Use BEFORE adding, moving or deleting any /api/* route, and whenever someone proposes editing api.go to add an endpoint. Do NOT use for MCP tools (use /mcp-tools), AI explain surfaces (/copilot-surface), or ClickHouse query design (/clickhouse-schema).
---

# /api-route — yeni endpoint, kendi dosyasında

`internal/api/api.go` on binin üstünde satır ve yüzlerce route taşıyor. Yeni yüzey oraya
yazılmaz: kendi dosyasını açar, `api.go` **tek satır** büyür. Bu bir
temenni değil, kodda yazılı direktif — `vmetrics_routes.go:5-8`,
`ai_routes.go:11-13`, `api.go:1083-1085` ("Yeni AI ucu ORAYA eklenir,
api.go'ya değil").

Ölçüldü: yeni dosya eklemenin maliyeti **1 kayıt satırı**. `buildMux`,
import listesi, OpenAPI, middleware zinciri — hiçbiri değişmiyor.

Bu skill v0.9.1293'te 438 route + 131 dosyanın taranmasıyla yazıldı;
her dosya:satır atıfı o gün doğrulandı. Satır numaraları kayabilir —
sembol adları kaymaz, önce onları grep'le.

## Adım adım

### 1. Dosyayı seç
- Handler'lar aynı dosyada duracak → **`internal/api/<domain>.go`**
  (12/16 dosya böyle: `external.go`, `hosts.go`, `tempo.go`…)
- Handler'lar ayrı dosyada olacak (10+ handler) → **`<domain>_routes.go`
  + `<domain>_handlers.go`** (`ai_routes.go`, `vmetrics_routes.go`)

Mevcut 12 düz dosya YENİDEN ADLANDIRILMAZ — kural yeni dosyalar için.

### 2. Dosya başlığı — gerekçeli yorum ZORUNLU
`package api` → boş satır → yorum bloğu → `import`. Yorum sürüm damgalı
(14/16) ve **"neden böyle" + "neden öteki türlü DEĞİL"** yazar. Bu
repo'nun imzası; tek satırlık "widgets handlers" yorumu yetersiz.
Emsal: `vmetrics_routes.go:10-14`, `admin_rollup.go:7-11`.

### 3. Register fonksiyonu — dosyanın İLK bildirimi
```go
func (s *Server) registerWidgetRoutes(mux *http.ServeMux) {
```
**Alıcı metot olacak** (15/17, en yeni iki dosya da böyle). Serbest form
`registerXxxRoutes(mux, s *Server)` tek güne sıkışmış 2 dosyalık sapma
(2026-07-30, `rollup_routes.go` + `annotation_routes.go`) — taklit etme.

Register handler'lardan ÖNCE gelir (16/16): okuyucu dosyayı açar açmaz
yüzeyin tamamını görür.

### 4. Route yaz
- **Metot öneki zorunlu**: `mux.HandleFunc("GET /api/widgets", …)`.
  Metotsuz kayıt üç şeyi bozar: `auth.SkipPath`'in metot-duyarlı
  muafiyetleri (`GET /api/branding` public, `PUT` değil), `make audit`
  CHECK 7 regex'i (`"[A-Z]+ /` arıyor), ve Go 1.22 mux'ta kazara tüm
  metotları açar. Pakette `switch r.Method` deseni **0**.
- **`/api/` altında kal.** Dışına çıkan her yol `auth.go:290-293`
  catch-all'ı yüzünden **otomatik public** olur: `RequireRole` yazsan
  bile middleware kimlik çözmez, `FromContext` nil döner, `s.audit`
  sessizce hiçbir şey yazmaz. `/api/` dışındaki her route bir dış
  protokol sözleşmesidir (`/tempo/*` Grafana, `/v1/*` OTLP).
- Statik segmentler kebab-case (underscore 0, sondaki `/` 0); parametre
  adı AİLEYE uyar — servis kimliği `{name}`'dir
  (`/api/services/{name}/…`, 20 route), `{id}` değil. `{id}` genel
  kaynak kimliği (77 route).

### 5. Rol kapısı KAYIT satırında, handler'da değil
```go
mux.HandleFunc("GET /api/widgets", s.getWidgets)                        // viewer görür
mux.Handle("PUT /api/widgets/{id}",
    auth.RequireAnyRole(editorRoles, http.HandlerFunc(s.putWidget)))    // editor+
mux.Handle("DELETE /api/widgets/{id}",
    auth.RequireRole(auth.RoleAdmin, http.HandlerFunc(s.deleteWidget)))
```
`editorRoles` = `api.go:519` (`{RoleAdmin, RoleEditor}`). Kapıyı route
tablosunda görmek güvenlik incelemesini tek dosyaya indirir; handler-içi
kapı yalnız **sahiplik** kararı içindir (rol değil).

Okuma kardeşini kapısız bırak — viewer state'i GÖRMELİ, boş sayfa değil
(CLAUDE.md). Kanonik blok: `api.go:1210-1214` (GET çıplak,
POST/PUT/DELETE `RequireAnyRole`).

### 6. `api.go`'ya tek satır

> v0.10.247 — alternatif: `internal/api/route_registry.go` defteri. Domain dosyası
> `func init() { registerRoutesExtra("<ad>", (*Server).registerXxxRoutes) }` yazar,
> api.go'ya satır GİRMEZ; `buildMux` defteri ad sırasıyla boşaltır ve
> `TestMuxRoutePatterns` çakışmayı görür (`preferences_routes.go`,
> `admin_function_id.go` emsalleri). Operatörün "api.go'ya satır ekleme" kısıtı
> geldiğinde bu yol.
```go
s.registerWidgetRoutes(mux) // v0.10.X — widget drill-down, widgets.go
```

| Yüzey | Nereye |
|---|---|
| Okuma / drill-down (en yaygın) | `api.go:832-839` bloğunun **en altına** |
| `/api/settings/*` | `api.go:1156` civarı, `registerVMetricsRoutes` komşusu |
| `/api/admin/clickhouse/*` | `api.go:632` / `:636` |
| AI / copilot ucu | **api.go'ya HİÇ dokunma** → `ai_routes.go:68` içine |

> ⚠️ Satırı bloğun ARASINA sokma. `api.go:834`'teki yorum bugün yanlış
> satırı anlatıyor (`registerAPITokenRoutes` üstünde `pivot.go`
> anlatımı): iki commit boyunca yeni çağrılar yorumlu satırın ÜSTÜNE
> eklendi, yorum aşağı kaydı. En alta ekle ya da satır-ÜSTÜ yorum
> kullan (`api.go:552-556` tarzı).

### 7. Frontend — dört dokunuş (CLAUDE.md "2" der, gerçek 4)
1. `lib/types.ts` — Go payload'ının aynası; başlıkta hangi Go tipini
   yansıttığı yazılı, `omitempty`→`?:`, nil-tolerant bölüm→`| null`
2. `lib/api.ts` — `get<T>`; ⚠️ `qs()` `undefined|null|''|false`
   parametreleri **ATAR**, bayrak göndereceksen `sig?: '1'`
3. `lib/queries/<domain>.ts` — `staleTime` **sunucu TTL'ine eşit**,
   `enabled:` ile fetch-on-open (ES-maliyet disiplini)
4. `lib/queries/index.ts` barrel

Yeni hook `lib/queries/cancellation.test.ts` listesine kaydolmalı —
her hook ailesinin `signal` geçirdiğini doğrulayan kapı.

## İskelet — okuma ucu

```go
package api

// widgets.go — v0.10.XXXX (<hangi brief / hangi sayfa>).
//
// api.go BÜYÜMEYECEK kuralı (registerVMetricsRoutes emsali): yüzeyin
// rotaları kendi dosyasında, api.go tek satır register çağrısıyla büyür.
//
//   GET /api/widgets?service=&from=&to=&env=&cluster=&limit=
//
// Rol kapısı YOK — salt-okunur drill-down (endpoints_detail.go duruşu);
// küresel middleware (api.go:1358) kimliksiz isteği zaten 401 yapıyor ve
// viewer bu veriyi GÖRMELİ. serveCached 30s; anahtar TÜM girdileri taşır
// (v0.5.187) ve pencere cacheBucket ile 30s grid'e oturur.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cilcenk/coremetry/internal/chstore"
)

func (s *Server) registerWidgetRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/widgets", s.getWidgets)
}

// widgetsKey — SAF: regresyon testi (cache_key_test.go emsali) bunu
// doğrudan pinler. limit anahtarda çünkü cevabın UZUNLUĞUNU değiştirir;
// env+cluster anahtarda çünkü eksikliği bir operatörün uat çekmecesini
// başkasının prod'una servis eder (endpoints_detail.go:63-66).
func widgetsKey(service, env, cluster string, limit int, from, to time.Time) string {
	return fmt.Sprintf("widgets:svc=%s:env=%s:clu=%s:lim=%d:w=%s",
		service, env, cluster, limit, cacheBucket(from, to))
}

func (s *Server) getWidgets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	service := strings.TrimSpace(q.Get("service"))
	if service == "" {
		writeJSONError(w, http.StatusBadRequest, "service parametresi zorunlu")
		return
	}
	from, to := parseFromTo(r, time.Hour)
	env := strings.TrimSpace(q.Get("env"))
	cluster := strings.TrimSpace(q.Get("cluster"))

	// Clamp anahtarın ÖNÜNDE: crafted ?limit= sınırsız ayrı cache girdisi
	// basmasın (endpoints_detail.go:323-329).
	limit := parseInt(q.Get("limit"), 20)
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	key := widgetsKey(service, env, cluster, limit, from, to)
	s.serveCached(w, r, key, 30*time.Second, func(ctx context.Context) (any, error) {
		// Kapalı gelen ctx'i KULLAN. r.Context()'e closure kurmak
		// v0.8.319 bug'ıydı: SWR arka-plan tazelemesi her seferinde
		// context.Canceled ile ölüyordu (cache.go:209-216).
		rows, err := s.store.Widgets(ctx, chstore.WidgetQuery{
			Service: service, From: from, To: to, Env: env, Cluster: cluster,
		}, limit)
		if err != nil {
			return nil, err // serveCached → writeErr
		}
		if rows == nil {
			rows = []chstore.WidgetRow{} // null yerine [] — FE .map()'liyor
		}
		return map[string]any{"items": rows}, nil
	})
}
```

`Widget*` tipleri ve `s.store.Widgets` yer tutucudur — `internal/chstore/`
tarafındaki gerçek adlarla değiştir (önce `/clickhouse-schema`). Diğer
her sembol repo'da doğrulanmıştır.

## İskelet — yazma ucu

```go
const widgetNameMax = 80

func (s *Server) putWidget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct { // tek handler → anonim; 2+ handler paylaşıyorsa `widgetInput`
		Label   string `json:"label"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSONError(w, http.StatusBadRequest, "geçersiz JSON: "+err.Error())
		return
	}
	in.Label = strings.TrimSpace(in.Label)
	if in.Label == "" || len(in.Label) > widgetNameMax {
		writeJSONError(w, http.StatusBadRequest, "label 1-80 karakter olmalı")
		return
	}
	if err := s.store.SaveWidget(r.Context(), id, in.Label, in.Enabled); err != nil {
		writeErr(w, err)
		return
	}
	// Audit store BAŞARILI döndükten SONRA, writeJSON'dan ÖNCE.
	// İmza: audit(r, action, kind, targetID, details) — anomaly_extra.go:275.
	// CLAUDE.md'deki yazım parametre ADLARINI ters gösteriyor (2. arg action,
	// 3. arg kind); değer sırası doğru. Emsal: api_monitors.go:131.
	s.audit(r, "widget.update", "widget", id,
		fmt.Sprintf("label=%q enabled=%v", in.Label, in.Enabled))
	writeJSON(w, map[string]any{"ok": true})
}
```

Yazma işini bir yardımcıya delege ediyorsan audit'i YARDIMCIYA koy —
iki uçtan biri unutulmasın (`runbook.go:124` emsali).

## Gövde çözme + tip yeri

`json.NewDecoder(r.Body).Decode(&x)` — 95/95 çözme noktası böyle.
**Tek handler kullanıyorsa anonim inline struct; iki+ handler
paylaşıyorsa adlandırılmış tip** (paket-içi request tiplerinin 10'unun
10'u da paylaşımlı: `esSettingsInput`, `vmSettingsInput`,
`devopsSettingsInput`).

Adlandırma: request `…Input` > `…Request`; response `…Response`;
`…Payload` yalnız çok bölümlü drawer yükü. 97/150 tip unexported —
küçük harf varsayılan. **Merkezi `types.go` YOK**: tip handler'la aynı
dosyada, kullanımından hemen önce.

Yanıt şekli: tek listeli/basit cevap → `map[string]any`; çok bölümlü
sözleşme → tipli struct (`endpointDetailPayload` emsali).

## Hata yazımı

| Durum | Çağrı | Not |
|---|---|---|
| Doğrulama / kötü girdi | `writeJSONError(w, 400, msg)` | `api.go:11609` |
| Store / upstream hatası | `writeErr(w, err)` | `api.go:11622` |
| Başarı | `writeJSON(w, v)` | `api.go:11588`, yalnız 200 |

`writeErr` dalları: `context.Canceled` → **499 gövdesiz** (5xx sayılırsa
Coremetry'nin KENDİ `error_rate` anomalisini tetikler, v0.7.13) ·
`errNotFound` → 404 · `errUpstream` → 502 + log · `errBadRequest` → 400 ·
varsayılan → 500 + ham `err.Error()`.

`http.Error` **yeni kodda kullanma**: stdlib `text/plain` yazar, pakette
83 satır JSON gövdeyi düz-metin başlığıyla gönderiyor (`writeJSONError`
tam bunu kapatmak için yazıldı, v0.9.803). Tek istisna: bilinçli
düz-metin 503 bağımlılık mesajları (`tempo_handlers.go:34`).

**Sızıntı uyarısı:** `writeErr` varsayılan dalı ham `err.Error()` döner
ve rol kapısızdır — viewer da CH host/port/tablo/`code: NNN` görür.
Temiz mesaj istiyorsan sentinel sarmala: `fmt.Errorf("%w: …", errBadRequest)`.

**Test/probe uçları istisnası:** bir bağlantı denemesinin başarısızlığı
operatörün sorusuna BAŞARILI bir cevaptır — `200 + {ok:false, error}`
dön, 4xx değil (`vmetrics_handlers.go:161-164`).

## "api.go'ya endpoint ekleyelim" denirse

**Cevap hayır — ama nedenini ölç, dogma satma.**

**api.go'da KALMASI gerekenler** (taşınırsa bozulur):
`POST /v1/{traces,logs,metrics}` (üçü tek `otlpHandler` değerini
paylaşıyor) · `GET /api/events` (`if s.bus != nil` koşullu) ·
`/api/mcp/*` (`if s.mcp != nil`; handler'lar `*mcp.Server` metodu) ·
`GET /livez` (pakette tek inline closure) · `mux.Handle("/",
spaHandler(sub))` (`api.go:1338`, kayıt sırası SON olmalı).

**Taşıma yordamı** (mevcut aileyi api.go'dan çıkarmak için):
1. Yeni `<domain>.go` / `<domain>_routes.go` aç
2. Kayıt satırlarını **birebir** kopyala (metot öneki + hizalama + auth
   wrapper dahil)
3. api.go'dan sil, yerine tek satır çağrı
4. Gate'leri koş; **bölünmüş kayıt bırakma** (bugün 5 aile iki yerden
   kayıtlı: `/api/endpoints`, `/api/databases`, `/api/traces`,
   `/api/spans`, `/api/admin/clickhouse`)

Mekanik aileler (handler'ı %100 tek dosyada): `/api/topology` 11 route ·
`/api/logs` 9 · `/api/incidents` 9. Bağlam: api.go'daki route'ların
**yaklaşık yarısının handler'ı zaten başka dosyada** — `registerRoutes`'un yarısı
saf pas-geçme kaydı, taşımanın davranışsal riski sıfır.

## Sessizce bozulanlar

- 🔴 **Kayıt satırı unutulursa 404 DEĞİL, HTTP 200 + boş ekran.**
  SPA catch-all (`api.go:1338`) yolu `index.html`'e düşürür → FE
  `request()` JSON olmayan content-type'ta `undefined` döner
  (`frontend/src/lib/api.ts:331`). Hata fırlatılmaz, log basılmaz,
  network panelinde 200 görünür; `go build`/`go test`/`make audit`
  üçü de yeşil. **Kontrol:**
  `grep -n "registerWidgetRoutes" internal/api/api.go`
- 🔴 **Eksik `s.audit` — hiçbir kapı yakalamaz.** `make audit`'in 9
  CHECK'inin hiçbiri audit çağrısı aramıyor; `go vet`/`go test` de
  göremez. Tamamen insan-inceleme kalemi. (`s.audit` ayrıca
  best-effort: kanal doluysa log+drop, `claims == nil` ise sessizce
  döner.)
- 🔴 **Cache anahtarında eksik girdi (v0.5.187 sınıfı).** CHECK 1 yalnız
  yazılmış `len(` kalıbını yakalar; "env'i anahtara koymayı unuttum"u
  göremez. Çare: anahtarı SAF fonksiyona ayır + `*_key_test.go`
  (kanonik: `internal/api/cache_key_test.go` — distinctness, stability,
  permutation invariance).
- 🔴 **Metotsuz kayıt** — CHECK 7 regex'i `"[A-Z]+ /` istediği için
  hiçbir kapı yakalamaz.
- 🟠 **`/api/` dışı yol** → sessiz public; wrapper'ın olsa bile
  middleware kimlik çözmez.
- 🟠 **Auth wrapper unutulursa** anonim erişim YOK (401 yine gelir) ama
  **rol ayrımı da yok**: viewer admin verisini okur/yazar.
- 🟡 **`serveCached` closure'ında `r.Context()`** → SWR ölümü (v0.8.319).
- 🟡 **Boş dilim `null` dönerse** FE'de `undefined.map()`; `rows == nil`
  → `[]T{}`.

## Gate'ler

```bash
go build ./...
go test ./internal/api/ -run 'TestMuxRoutePatterns' -v   # bu değişikliğin EN kritik komutu
go test ./...
make audit                                                # CHECK 7 kopya route
cd frontend && npx tsc --noEmit                           # FE'ye dokunduysan
grep -n "registerWidgetRoutes" internal/api/api.go        # S1 — otomatik kapısı YOK
```

AI ucu eklediysen ek olarak:
`go test ./internal/api/ -run 'TestRequireCopilotRouteCoverage|TestNoInlineCopilotGates' -v`

> ⚠️ `TestMuxRoutePatterns` **4 route'u kapsamıyor**: test `&Server{}` ile
> kurulduğu için `s.bus`/`s.mcp` nil, `/api/events` ve `/api/mcp/*`
> kaydedilmiyor (`mux_routes_test.go:18`). Bunlarla çakışan yeni bir
> kalıp testi geçer, **prod boot'ta panic'ler** — testin yazılma sebebi
> olan v0.9.465-470 olayının aynısı. O dört yolla çakışan bir kalıp
> yazıyorsan elle kontrol et.

Sonrası CLAUDE.md akışı: `/review-changes` (non-trivial diff) → `/release`.
