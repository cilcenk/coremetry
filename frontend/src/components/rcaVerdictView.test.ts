// v0.9.560 — RCA verdict panelinin dürüstlük kuralları.
//
// Her test bir YANLIŞ OKUMA senaryosunu engelliyor. Sorular
// "fonksiyon çalışıyor mu" değil, "operatör yanlış bir şey okur mu".
import { describe, it, expect } from 'vitest';
import {
  verdictTone,
  verdictIsDegraded,
  verdictHasShieldWarning,
  measuredText,
} from './rcaVerdictView';
import type { RCAVerdict } from '@/lib/types';

const base: RCAVerdict = {
  verdict: 'probable_cause',
  title: 't', summary: 's',
  rootCause: { entity: 'a-svc', failure_mode: '', trigger: '', latent_weakness: '', evidence: [] },
  confidence: 0.5, modelConfidence: 0.9, hypothesisConfidence: 0.4,
  shields: { parsed: true },
};

describe('verdictTone', () => {
  it('kanıt yetersiz KIRMIZI değil', () => {
    // Kırmızı, "kanıt yetersiz"i bir başarısızlık gibi gösterir.
    // Modeli bu cevaptan kaçınmaya iten sinyal, tam da istemediğimiz şey:
    // yanlış ve kendinden emin bir karar, tüm platforma olan güveni yıkar.
    expect(verdictTone('insufficient_evidence')).toBe('neutral');
    expect(verdictTone('insufficient_evidence')).not.toBe('danger');
  });
  it('belirlenen kök neden yeşil, olası neden sarı', () => {
    expect(verdictTone('root_cause_identified')).toBe('success');
    expect(verdictTone('probable_cause')).toBe('warning');
  });
});

describe('verdictIsDegraded', () => {
  it('parsed=false → degrade UYARISI', () => {
    // Bu uyarı olmadan "kanıt yetersiz" bir BULGU sanılır; oysa model
    // hiç cevap verememiştir. İkisi çok farklı şeyler.
    expect(verdictIsDegraded({ ...base, shields: { parsed: false } })).toBe(true);
  });
  it('parsed=true → uyarı yok', () => {
    expect(verdictIsDegraded(base)).toBe(false);
  });
  it('shields alanı hiç yoksa degrade SAYILMAZ', () => {
    // Eski bir yanıt (shields'sız) uyarı basmamalı — sahte alarm,
    // gerçek alarmı değersizleştirir.
    const legacy = { ...base, shields: undefined } as unknown as RCAVerdict;
    expect(verdictIsDegraded(legacy)).toBe(false);
  });
});

describe('verdictHasShieldWarning', () => {
  it('uydurma varlık yakalandıysa uyarı', () => {
    expect(verdictHasShieldWarning({
      ...base, shields: { parsed: true, unknownEntities: ['hayalet-svc'] },
    })).toBe(true);
  });
  it('geçersiz kanıt atfı varsa uyarı', () => {
    expect(verdictHasShieldWarning({
      ...base, shields: { parsed: true, rejectedEvidence: ['N1'] },
    })).toBe(true);
  });
  it('geçersiz eleme varsa uyarı', () => {
    expect(verdictHasShieldWarning({
      ...base, shields: { parsed: true, refutationInvalid: true },
    })).toBe(true);
  });
  it('temiz verdict → uyarı yok', () => {
    expect(verdictHasShieldWarning(base)).toBe(false);
  });
  it('boş diziler uyarı SAYILMAZ', () => {
    expect(verdictHasShieldWarning({
      ...base, shields: { parsed: true, unknownEntities: [], rejectedEvidence: [] },
    })).toBe(false);
  });
});

describe('measuredText', () => {
  it('null → ölçülemedi (SIFIR DEĞİL)', () => {
    // Ekranda "0 hata" yazmak, tam patlama anında "etkilenen istek yok"
    // demektir — operatörün okuyacağı en yanlış cümle.
    expect(measuredText(null, String)).toBeNull();
    expect(measuredText(undefined, String)).toBeNull();
  });
  it('gerçek sıfır ÖLÇÜLMÜŞ sayılır', () => {
    // Gerçek bir sıfır (trafik yok) ile ölçülememiş olmak farklı.
    expect(measuredText(0, n => `${n}`)).toBe('0');
  });
  it('biçimlendirici uygulanır', () => {
    expect(measuredText(0.1234, n => `%${(n * 100).toFixed(2)}`)).toBe('%12.34');
  });
});
