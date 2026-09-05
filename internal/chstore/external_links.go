package chstore

// external_links.go — v0.10.345 (operatör: "function_id'yi içerideki log
// izleme platformuna direkt linkle erişebilir hale getirebilir miyim;
// channel_code da gerekiyormuş; trace'te bu bilgiler varsa Explain trace gibi
// bir düğmeyle ulaşalım").
//
// Admin, trace sayfasında görünecek DIŞ LİNK şablonları tanımlar
// (`system_settings['external_links']`). Şablon değişkenleri trace'in
// span'lerinden çözülür; hepsi çözülüyorsa düğme etkin, değilse eksik
// alanları söyleyerek pasif. Host adı REPOYA GİRMEZ — ayarda yaşar.
//
//	{{attr.KEY}}            span attribute değeri (kök span önce), URL-kodlu
//	{{attrTime.KEY:FMT}}    attribute değerinin içindeki yyyyMMddHHmmss zamanı,
//	                        FMT ile yeniden biçimlenir (dd MM yyyy yy HH mm ss)
//	{{time:FMT}}            trace başlangıcı (tarayıcı yerel saati), FMT
//	{{endTime:FMT}}         trace bitişi (en geç span sonu), FMT — v0.10.371
//	{{traceId}} {{service}} kimlik
//
// Örnek (log platformu): .../masterlog?date={{time:ddMMyyyyHHmm}}
//   &functionId={{attr.function_id}}&channelCode={{attr.channel_code}}
//
// Doğrulama burada (sınırda): yalnız http(s), bilinmeyen değişken/format
// tokenı reddedilir — bozuk şablon kaydedilmez ki düğme hiç yanlış URL
// açmasın. Render istemcide (lib/externalLinks.ts, saf + test).

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
)

const externalLinksSettingKey = "external_links"

const (
	externalLinksMax        = 16
	externalLinkLabelMax    = 64
	externalLinkTemplateMax = 2048
)

// ExternalLink — bir düğme.
type ExternalLink struct {
	Label       string `json:"label"`
	URLTemplate string `json:"urlTemplate"`
	// Color — v0.10.346 (operatör: "Log İzleme düğmesi kırmızı, içi beyaz
	// yazı, aracın renklerine uygun"): düğme dolgu rengi (#rrggbb); boş =
	// ikincil düğme. Yazı rengi --on-accent (beyaz), araç başına marka rengi.
	Color string `json:"color,omitempty"`
	// Requires — türetilmiş: şablondaki attribute anahtarları (attr.* ve
	// attrTime.*); istemci hepsi çözülmeden düğmeyi etkinleştirmez.
	Requires []string `json:"requires,omitempty"`
}

// ExternalLinkSettings — blob.
type ExternalLinkSettings struct {
	Links []ExternalLink `json:"links"`
}

var externalLinksPtr atomic.Pointer[[]ExternalLink]

var (
	// Anahtar sınıfında ':' YOK: {{attrTime.function_id:ddMM}} — ':' biçim ayracı.
	externalLinkVarRe   = regexp.MustCompile(`\{\{\s*([A-Za-z]+)(?:\.([A-Za-z0-9_.-]+))?(?::([A-Za-z]+))?\s*\}\}`)
	externalLinkFmtRe   = regexp.MustCompile(`^(dd|MM|yyyy|yy|HH|mm|ss)+$`)
	externalLinkColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// ExternalLinkVars — SAF: şablondaki değişkenler → gerekli attribute
// anahtarları; hata = bilinmeyen değişken / biçim.
func ExternalLinkVars(tpl string) ([]string, error) {
	seen := map[string]bool{}
	var req []string
	for _, m := range externalLinkVarRe.FindAllStringSubmatch(tpl, -1) {
		kind, key, format := m[1], m[2], m[3]
		switch kind {
		case "attr":
			if key == "" || format != "" {
				return nil, fmt.Errorf("{{attr.KEY}} bekleniyor: %s", m[0])
			}
		case "attrTime":
			if key == "" || format == "" || !externalLinkFmtRe.MatchString(format) {
				return nil, fmt.Errorf("{{attrTime.KEY:FMT}} bekleniyor (FMT: dd MM yyyy yy HH mm ss): %s", m[0])
			}
		case "time", "endTime":
			// endTime — v0.10.371: trace bitişi; log platformunun dakika
			// penceresi başlangıçtan/kimlik zamanından sonra biten trace'i
			// kaçırıyordu.
			if key != "" || format == "" || !externalLinkFmtRe.MatchString(format) {
				return nil, fmt.Errorf("{{%s:FMT}} bekleniyor (FMT: dd MM yyyy yy HH mm ss): %s", kind, m[0])
			}
		case "traceId", "service":
			if key != "" || format != "" {
				return nil, fmt.Errorf("{{%s}} argüman almaz: %s", kind, m[0])
			}
		default:
			return nil, fmt.Errorf("bilinmeyen değişken: %s", m[0])
		}
		if key != "" && !seen[key] {
			seen[key] = true
			req = append(req, key)
		}
	}
	return req, nil
}

// NormalizeExternalLinks — SAF: doğrula + türet.
func NormalizeExternalLinks(in ExternalLinkSettings) (ExternalLinkSettings, error) {
	out := ExternalLinkSettings{Links: []ExternalLink{}}
	if len(in.Links) > externalLinksMax {
		return out, fmt.Errorf("en çok %d link", externalLinksMax)
	}
	labels := map[string]bool{}
	for i, l := range in.Links {
		label := strings.TrimSpace(l.Label)
		tpl := strings.TrimSpace(l.URLTemplate)
		if label == "" || len(label) > externalLinkLabelMax {
			return out, fmt.Errorf("link %d: etiket 1-%d karakter", i+1, externalLinkLabelMax)
		}
		if labels[label] {
			return out, fmt.Errorf("link %d: etiket tekrar ediyor: %q", i+1, label)
		}
		labels[label] = true
		if tpl == "" || len(tpl) > externalLinkTemplateMax {
			return out, fmt.Errorf("link %d: şablon 1-%d karakter", i+1, externalLinkTemplateMax)
		}
		if !strings.HasPrefix(tpl, "https://") && !strings.HasPrefix(tpl, "http://") {
			return out, fmt.Errorf("link %d: şablon http(s):// ile başlamalı", i+1)
		}
		if strings.ContainsAny(tpl, " \t\r\n\"'<>") {
			return out, fmt.Errorf("link %d: şablonda boşluk/tırnak/<> olamaz", i+1)
		}
		req, err := ExternalLinkVars(tpl)
		if err != nil {
			return out, fmt.Errorf("link %d (%s): %v", i+1, label, err)
		}
		color := strings.ToLower(strings.TrimSpace(l.Color))
		if color != "" && !externalLinkColorRe.MatchString(color) {
			return out, fmt.Errorf("link %d (%s): renk #rrggbb biçiminde olmalı", i+1, label)
		}
		out.Links = append(out.Links, ExternalLink{Label: label, URLTemplate: tpl, Requires: req, Color: color})
	}
	return out, nil
}

func registerExternalLinks(cfg ExternalLinkSettings) {
	list := append([]ExternalLink(nil), cfg.Links...)
	externalLinksPtr.Store(&list)
}

// CurrentExternalLinks — kayıttakiler (her rol okur).
func CurrentExternalLinks() []ExternalLink {
	if p := externalLinksPtr.Load(); p != nil {
		return append([]ExternalLink{}, (*p)...)
	}
	return []ExternalLink{}
}

// LoadExternalLinks — boot; bozuk blob boot'u durdurmaz (çağıran loglar).
func (s *Store) LoadExternalLinks(ctx context.Context) error {
	raw, err := s.GetSetting(ctx, externalLinksSettingKey)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		registerExternalLinks(ExternalLinkSettings{})
		return nil
	}
	var in ExternalLinkSettings
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("external_links decode: %w", err)
	}
	cfg, err := NormalizeExternalLinks(in)
	if err != nil {
		return fmt.Errorf("external_links: %w", err)
	}
	registerExternalLinks(cfg)
	return nil
}

// SaveExternalLinks — doğrula + yaz + yayınla.
func (s *Store) SaveExternalLinks(ctx context.Context, in ExternalLinkSettings) (ExternalLinkSettings, error) {
	cfg, err := NormalizeExternalLinks(in)
	if err != nil {
		return cfg, err
	}
	raw, _ := json.Marshal(cfg)
	if err := s.PutSetting(ctx, externalLinksSettingKey, raw); err != nil {
		return cfg, err
	}
	registerExternalLinks(cfg)
	return cfg, nil
}
