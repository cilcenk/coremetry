// @vitest-environment jsdom
import { describe, it, expect } from 'vitest';
import { isActivationTarget } from './keyboard';

// v0.9.863 — UX denetimi K13: klavye-nav'lı HER sayfada (/services, /traces,
// /inbox, /logs…) Tab ile bir butona gelip Enter'a basınca hiçbir şey
// olmuyordu; j/k ile bir satır seçiliyse Enter butonu değil SATIRI açıyordu.
// Klavyeyle aksiyon akışı bu sayfalarda fiilen kırıktı.
//
// Kök neden: useTableNav Enter'ı GLOBAL registry'e kaydediyor, dispatch de
// eşleşen her kısayolda KOŞULSUZ preventDefault() çağırıyordu. Tarayıcı odaklı
// bir butonda Enter'ı ancak varsayılan iptal EDİLMEZSE click'e çevirir — yani
// sayfanın satır kısayolu butonun kendi aktivasyonunu yutuyordu.
//
// Düzeltmenin asıl inceliği ve bu testlerin yarısının sebebi: çözüm
// isEditableTarget'a BUTTON eklemek DEĞİL. O bayrak tüm kısayolları kapatır ve
// bir toolbar butonuna tıkladıktan sonra odak orada kaldığı için j/k
// gezinmesini o andan itibaren öldürürdü — bir bug'ı daha sinsi bir bug'la
// değişmek. Susturulan yalnız AKTİVASYON tuşları, yalnız aktive edilebilir bir
// eleman odaktayken.

const make = (html: string): HTMLElement => {
  document.body.innerHTML = html;
  return document.body.firstElementChild as HTMLElement;
};

describe('isActivationTarget — K13 yutulan Enter', () => {
  it('buton odaktayken Enter\'ı elemana GERİ VERİR', () => {
    expect(isActivationTarget(make('<button>Save</button>'), 'Enter')).toBe(true);
  });

  it('href\'li link odaktayken Enter\'ı geri verir', () => {
    expect(isActivationTarget(make('<a href="/x">go</a>'), 'Enter')).toBe(true);
  });

  it('role="button" ve summary de aktive edilebilir', () => {
    expect(isActivationTarget(make('<div role="button">go</div>'), 'Enter')).toBe(true);
    expect(isActivationTarget(make('<summary>more</summary>'), 'Enter')).toBe(true);
  });

  it('butonun İÇİNDEKİ eleman hedefse de sayılır (ikon/span sarmalayıcı)', () => {
    // Gerçek DOM'da e.target çoğu zaman butonun içindeki <span>/<svg>'dir;
    // closest() olmadan düzeltme pratikte hiç devreye girmezdi.
    make('<button><span id="ico">✓</span> Save</button>');
    expect(isActivationTarget(document.getElementById('ico'), 'Enter')).toBe(true);
  });

  it('href\'SİZ <a> aktive edilmez — kısayol çalışmalı', () => {
    expect(isActivationTarget(make('<a>not a link</a>'), 'Enter')).toBe(false);
  });

  it('devre dışı buton hiçbir şey aktive etmez — kısayol çalışmalı', () => {
    expect(isActivationTarget(make('<button disabled>Save</button>'), 'Enter')).toBe(false);
  });

  it('sıradan eleman odaktayken kısayol AYNEN çalışır', () => {
    expect(isActivationTarget(make('<tr><td>row</td></tr>'), 'Enter')).toBe(false);
    expect(isActivationTarget(make('<div>plain</div>'), 'Enter')).toBe(false);
    expect(isActivationTarget(null, 'Enter')).toBe(false);
  });

  describe('yalnız aktivasyon tuşları susturulur', () => {
    // BU, düzeltmenin blanket muafiyet OLMADIĞININ pini. Bir butona tıkladıktan
    // sonra odak orada kalır; j/k/g/Escape hâlâ çalışmalı, yoksa gezinme
    // "butona her dokunulduğunda" ölürdü.
    const btn = () => make('<button>Save</button>');
    for (const combo of ['j', 'k', 'g', 'o', 'Escape', 'mod+k', '/', 'shift+g']) {
      it(`${combo} buton odaktayken de kısayol kalır`, () => {
        expect(isActivationTarget(btn(), combo)).toBe(false);
      });
    }
  });

  it('Space butonu aktive eder, LİNK\'i etmez', () => {
    // Tarayıcı Space'i butonda click'e çevirir; linkte sayfayı kaydırır.
    // İkisini aynı saymak, link odaklıyken bir Space kısayolunu boşuna
    // susturmak olurdu.
    expect(isActivationTarget(make('<button>Save</button>'), ' ')).toBe(true);
    expect(isActivationTarget(make('<div role="button">go</div>'), ' ')).toBe(true);
    expect(isActivationTarget(make('<a href="/x">go</a>'), ' ')).toBe(false);
  });
});
