// settingsLoadGate.test.ts — v0.9.938 (UX denetimi C4) regresyon pini.
//
// SEKİZ Settings sekmesi ayar okumasını `.catch(() => setLoaded(true))` ile
// yutuyordu: GET başarısız olunca form YÜKLENDİ sayılıp BOŞ çiziliyordu ve
// Kaydet aktifti. Tek tık mevcut ayarın üstüne boş değerler yazabilirdi —
// denetimin tek VERİ KAYBETTİREN bulgusu, üstelik sessiz: hiçbir yerde
// "okuyamadım" yazmıyordu.
//
// Neden KAYNAK pini: bu desen tek bir fonksiyonda yaşamıyor, sekiz dosyada
// tekrarlanıyordu ve dokuzuncu sekme kopyala-yapıştırla doğar. Davranış
// testi yalnız bugün dönüştürdüğümüz sekizini korur; kaynak pini yeni
// sekmeyi de yakalar.
import { describe, expect, it } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { resolve } from 'node:path';

const DIR = resolve(__dirname);
const tabs = readdirSync(DIR).filter(f => f.endsWith('Tab.tsx'));

describe('Settings okuma kapısı (v0.9.938)', () => {
  it('sekme dosyaları bulundu (test kendi kapsamını kaybetmesin)', () => {
    // Klasör yeniden düzenlenirse bu test sessizce SIFIR dosya tarayıp
    // yeşil kalırdı — koruma değil, koruma görüntüsü olurdu.
    expect(tabs.length).toBeGreaterThan(10);
  });

  it('hiçbir sekme okuma hatasını "yüklendi" saymıyor', () => {
    const offenders: string[] = [];
    for (const f of tabs) {
      const src = readFileSync(resolve(DIR, f), 'utf8');
      // Yakalanan hatayı doğrudan "yüklendi"ye çeviren her biçim.
      if (/catch\(\s*\(\)\s*=>\s*setLoaded\(true\)/.test(src)) offenders.push(f);
      if (/catch\(\s*\w*\s*=>\s*\{?\s*setLoaded\(true\)/.test(src)) offenders.push(f);
      // v0.9.1043 — aynı hatanın İKİNCİ yazımı (LdapTab'de yakalandı):
      // catch'te uydurma boş config basmak (`setCfg(emptyLDAP())`) da
      // formu "yüklendi" sayar — truthy boş nesne `if (!cfg)` kapısını
      // geçer, Kaydet aktif kalır ve PUT mevcut satırı ezer (LDAP'ta
      // üstüne enabled:false → org genelinde sessiz kapanış).
      if (/catch\(\s*\(?\w*\)?\s*=>\s*set\w+\(\s*empty\w*\(\)\s*\)/.test(src)) offenders.push(f);
    }
    expect(offenders, 'okuma hatasını yutan sekme(ler) — boş form kaydedilirse ' +
      'mevcut ayar ezilir').toEqual([]);
  });

  it('useSettingsLoad kullanan HER sekme kapıyı GERÇEKTEN çiziyor', () => {
    // useSettingsLoad'ı çağırıp hata dalını çizmemek, kapıyı kurup
    // kapısız bırakmak olurdu: loaded false kalır, sekme SONSUZ spinner
    // gösterirdi — sessizce daha da kötü bir hâl.
    // v0.9.1150 — liste artık TÜRETİLİYOR, elle sayılmıyor. Sabit
    // sekizli, yeni bir sekme (MetricsBackendTab) kapıyı doğru kursa bile
    // ONU ÖLÇMÜYORDU: kapı kapsamı göç sırasında sessizce erir ve
    // "yeşil" cevabı yalnızca eski dosyalar hakkında olur. Kapsamı
    // daraltmak yerine, kapıyı KULLANAN her dosyayı ölçüyoruz.
    const gated = tabs.filter(f =>
      readFileSync(resolve(DIR, f), 'utf8').includes('useSettingsLoad('));
    // Zemin: türetme bir gün sıfır dosya bulursa test yeşil kalıp hiçbir
    // şey korumazdı (yukarıdaki `tabs.length` pininin aynı gerekçesi).
    expect(gated.length, 'useSettingsLoad kullanan sekme bulunamadı').toBeGreaterThanOrEqual(10);
    for (const f of gated) {
      const src = readFileSync(resolve(DIR, f), 'utf8');
      expect(src, `${f}: hata dalı çizilmiyor`).toMatch(/if \(loadErr\) return <SettingsLoadError/);
      // Kapı, spinner'ın ÖNÜNDE olmalı: sonrasında olsaydı hata hâlinde
      // sonsuz spinner kazanırdı. İki yazılış var — `loaded` bayraklı
      // sekizli ve LdapTab'in `!cfg` biçimi (v0.9.1043).
      const spinnerGate = src.includes('if (!loaded)')
        ? src.indexOf('if (!loaded)')
        : src.indexOf('if (!cfg) return <Spinner');
      expect(spinnerGate, `${f}: spinner kapısı bulunamadı`).toBeGreaterThan(-1);
      expect(src.indexOf('if (loadErr)'),
        `${f}: hata kapısı spinner'dan SONRA — hata hâlinde sonsuz spinner`)
        .toBeLessThan(spinnerGate);
    }
  });

  it('LdapTab kapıyı çiziyor (v0.9.1043 — dokuzuncu yazım)', () => {
    // LdapTab sekizliden farklı: loaded bayrağı yok, `if (!cfg)` spinner'ı
    // var. Kapı yine spinner'ın ÖNÜNDE olmalı — hata hâlinde form da
    // sonsuz spinner da değil, SettingsLoadError çizilmeli.
    const src = readFileSync(resolve(DIR, 'LdapTab.tsx'), 'utf8');
    expect(src, 'LdapTab: useSettingsLoad kullanmıyor').toContain('useSettingsLoad(');
    expect(src, 'LdapTab: hata dalı çizilmiyor').toMatch(/if \(loadErr\) return <SettingsLoadError/);
    // Not: düz 'if (!cfg)' araması fix'in kendi yorum satırına takılır;
    // gerçek render kapısı Spinner dönüşüyle birlikte aranır.
    expect(src.indexOf('if (loadErr)')).toBeLessThan(src.indexOf('if (!cfg) return <Spinner'));
  });

  it('kapı hata hâlinde loaded\'ı true YAPMIYOR', () => {
    const src = readFileSync(resolve(DIR, 'shared.tsx'), 'utf8');
    const i = src.indexOf('export function useSettingsLoad');
    const body = src.slice(i, src.indexOf('export function SettingsLoadError'));
    expect(body).toContain('.catch(e => { if (!alive) return; setError(humanize(e)); });');
    expect(body).not.toMatch(/catch[\s\S]*setLoaded\(true\)/);
    // Yarış koruması (C3 sınıfı): geç dönen yanıt sökülmüş bileşene
    // yazmamalı.
    expect(body).toContain('let alive = true;');
    expect(body).toContain('return () => { alive = false; };');
  });
});
