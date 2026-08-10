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
    }
    expect(offenders, 'okuma hatasını yutan sekme(ler) — boş form kaydedilirse ' +
      'mevcut ayar ezilir').toEqual([]);
  });

  it('dönüştürülen sekiz sekme kapıyı GERÇEKTEN çiziyor', () => {
    // useSettingsLoad'ı çağırıp hata dalını çizmemek, kapıyı kurup
    // kapısız bırakmak olurdu: loaded false kalır, sekme SONSUZ spinner
    // gösterirdi — sessizce daha da kötü bir hâl.
    for (const f of ['AiTab.tsx', 'BrandingTab.tsx', 'ClustersTab.tsx', 'DevOpsTab.tsx',
      'ElasticTab.tsx', 'KibanaTab.tsx', 'LogBridgeTab.tsx', 'TempoTab.tsx']) {
      const src = readFileSync(resolve(DIR, f), 'utf8');
      expect(src, `${f}: useSettingsLoad kullanmıyor`).toContain('useSettingsLoad(');
      expect(src, `${f}: hata dalı çizilmiyor`).toMatch(/if \(loadErr\) return <SettingsLoadError/);
      // Kapı, spinner'ın ÖNÜNDE olmalı: sonrasında olsaydı hata hâlinde
      // sonsuz spinner kazanırdı.
      expect(src.indexOf('if (loadErr)')).toBeLessThan(src.indexOf('if (!loaded)'));
    }
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
