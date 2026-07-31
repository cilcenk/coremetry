import { describe, it, expect } from 'vitest';
import { scopedChartTitle, scopeTitleTip, ALL_SPANS_SUFFIX } from './scopeTitle';

// v0.9.483 — operatör: "· giriş" eki kafa karıştırıyor. Varsayılan başlık
// temiz, SÜRPRİZ olan (tüm span'lere düşme) ekranda etiketli kalır.

describe('scopedChartTitle', () => {
  it('varsayılan (giriş span kapsamı) → ek YOK', () => {
    for (const base of ['Response time', 'Throughput', 'Failure rate', 'Apdex']) {
      expect(scopedChartTitle(base, false)).toBe(base);
    }
  });

  it('fallback (usingAllSpans) → görünür "· tüm span’ler" eki', () => {
    expect(scopedChartTitle('Throughput', true)).toBe('Throughput' + ALL_SPANS_SUFFIX);
    expect(scopedChartTitle('Apdex', true)).toBe('Apdex · tüm span’ler');
  });

  it('eski "· giriş" eki hiçbir dalda geri gelmez', () => {
    for (const usingAll of [false, true]) {
      expect(scopedChartTitle('Response time', usingAll)).not.toContain('giriş');
    }
  });
});

describe('scopeTitleTip', () => {
  it('giriş kapsamında popülasyonu anlatır (server + consumer)', () => {
    const t = scopeTitleTip(false);
    expect(t).toContain('server + consumer');
    expect(t.toLocaleLowerCase('tr')).toContain('giriş span');
  });

  it('fallback\'te NEDEN düşüldüğünü söyler', () => {
    expect(scopeTitleTip(true)).toContain('yok');
  });

  it('iki dal farklı metin döndürür', () => {
    expect(scopeTitleTip(true)).not.toBe(scopeTitleTip(false));
  });
});
