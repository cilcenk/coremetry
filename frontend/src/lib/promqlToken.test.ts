// promqlToken tests (v0.9.766) — PromQL metrik-adı autocomplete'in saf
// çekirdeği. Tablo-güdümlü: imleç "|" ile işaretlenir, testte kesilir.
// Kritik davranışlar: noktalı OTel adı TEK token, süslü parantez içi
// (label matcher) null (Faz 2'nin işi), sayıyla başlayan token null
// ([5m] gibi aralıklar metrik adı değildir).

import { describe, it, expect } from 'vitest';
import {
  promqlTokenAt, replaceToken,
  promqlLabelContext, applyLabelKey, applyLabelValue,
  type PromqlLabelCtx,
} from './promqlToken';

// cursor("rate(http.ser|") → { text: 'rate(http.ser', pos: 13 }
function cursor(marked: string): { text: string; pos: number } {
  const pos = marked.indexOf('|');
  if (pos < 0) throw new Error(`test input has no cursor marker: ${marked}`);
  return { text: marked.slice(0, pos) + marked.slice(pos + 1), pos };
}

describe('promqlTokenAt', () => {
  const cases: Array<{ name: string; input: string; want: string | null }> = [
    // — dotted OTel names are ONE token —
    { name: 'dotted OTel name at end', input: 'http.server.request|', want: 'http.server.request' },
    { name: 'dotted name mid-typing', input: 'http.server.req|', want: 'http.server.req' },
    { name: 'trailing dot kept in token', input: 'http.|', want: 'http.' },
    { name: 'colon (recording rule) is a token char', input: 'job:rate5m|', want: 'job:rate5m' },
    { name: 'underscore name', input: 'process_cpu_seconds|', want: 'process_cpu_seconds' },

    // — partial name inside a function call —
    { name: 'partial inside rate()', input: 'rate(http.ser|', want: 'http.ser' },
    { name: 'partial inside nested call', input: 'sum(rate(http.ser|', want: 'http.ser' },
    { name: 'second arg of histogram_quantile', input: 'histogram_quantile(0.95, http.ser|', want: 'http.ser' },

    // — inside {} = label matcher, Faz 2's job —
    { name: 'inside braces → null', input: 'metric{serv|', want: null },
    { name: 'inside braces after a matcher → null', input: 'metric{a="b",serv|', want: null },
    { name: 'inside braces on a value → null', input: 'metric{a="che|', want: null },

    // — after }, suggestions resume —
    { name: 'after closing brace, new token', input: 'metric{a="b"} / http.ser|', want: 'http.ser' },
    { name: 'after closing brace inside a call', input: 'rate(metric{a="b"}[5m]) + http.ser|', want: 'http.ser' },

    // — numbers are never metric names —
    { name: 'range selector [5m → null', input: 'rate(http.server.duration[5m|', want: null },
    { name: 'bare number → null', input: 'histogram_quantile(0.95|', want: null },
    { name: 'offset duration → null', input: 'metric offset 30m|', want: null },

    // — no token under the cursor —
    { name: 'right after open paren → null', input: 'rate(|', want: null },
    { name: 'empty text → null', input: '|', want: null },
    { name: 'after whitespace → null', input: 'http.server |', want: null },
  ];

  for (const c of cases) {
    it(c.name, () => {
      const { text, pos } = cursor(c.input);
      const tok = promqlTokenAt(text, pos);
      expect(tok?.text ?? null).toBe(c.want);
      if (tok) {
        // start/end must actually bracket the token in the source text.
        expect(text.slice(tok.start, tok.end)).toBe(tok.text);
        expect(tok.end).toBe(pos);
      }
    });
  }

  it('cursor mid-text only reads backwards (no forward greed)', () => {
    const { text, pos } = cursor('http.ser|ver.duration');
    const tok = promqlTokenAt(text, pos);
    expect(tok?.text).toBe('http.ser');
    expect(tok?.end).toBe(pos);
  });

  it('out-of-range positions are null', () => {
    expect(promqlTokenAt('abc', -1)).toBeNull();
    expect(promqlTokenAt('abc', 4)).toBeNull();
  });
});

describe('replaceToken', () => {
  const cases: Array<{ name: string; input: string; pick: string; want: string; wantPos: number }> = [
    {
      name: 'completes a bare partial',
      input: 'http.ser|',
      pick: 'http.server.request.duration',
      want: 'http.server.request.duration',
      wantPos: 28,
    },
    {
      name: 'completes inside rate() keeping the tail',
      input: 'rate(http.ser|[5m])',
      pick: 'http.server.duration',
      want: 'rate(http.server.duration[5m])',
      wantPos: 25,
    },
    {
      name: 'completes the second arg, leaves the first',
      input: 'histogram_quantile(0.95, http.ser|)',
      pick: 'http.server.duration',
      want: 'histogram_quantile(0.95, http.server.duration)',
      wantPos: 45,
    },
    {
      name: 'shorter replacement pulls the cursor back',
      input: 'process_cpu_seconds_tot|al',
      pick: 'up',
      want: 'upal',
      wantPos: 2,
    },
  ];

  for (const c of cases) {
    it(c.name, () => {
      const { text, pos } = cursor(c.input);
      const tok = promqlTokenAt(text, pos);
      expect(tok).not.toBeNull();
      const out = replaceToken(text, tok!, c.pick);
      expect(out.text).toBe(c.want);
      expect(out.pos).toBe(c.wantPos);
      // The new cursor must sit exactly at the end of the inserted name.
      expect(out.text.slice(out.pos - c.pick.length, out.pos)).toBe(c.pick);
    });
  }
});

// ── Faz 2 (v0.9.771) — süslü parantez içi label bağlamı ────────────────────

describe('promqlLabelContext', () => {
  type Want = { phase: 'key' | 'value'; metric: string; key?: string; partial: string; quoted?: boolean };
  const cases: Array<{ name: string; input: string; want: Want | null }> = [
    // — anahtar fazı —
    { name: 'boş süslü → tüm anahtarlar', input: 'm{|', want: { phase: 'key', metric: 'm', partial: '' } },
    { name: 'anahtar parçası', input: 'm{htt|', want: { phase: 'key', metric: 'm', partial: 'htt' } },
    { name: 'virgülden sonra yeni anahtar', input: 'm{a="x",ser|', want: { phase: 'key', metric: 'm', partial: 'ser' } },
    { name: 'virgül + boşluk sonrası boş anahtar', input: 'm{a="x", |', want: { phase: 'key', metric: 'm', partial: '' } },
    { name: 'iç içe çağrıda metrik adı doğru', input: 'sum(rate(cpu{po|', want: { phase: 'key', metric: 'cpu', partial: 'po' } },
    { name: 'kapanan süslüden sonra yeni selector', input: 'm{a="b"} + n{k|', want: { phase: 'key', metric: 'n', partial: 'k' } },

    // — değer fazı —
    { name: 'tırnaksız değer (operatörden hemen sonra)', input: 'm{route=|', want: { phase: 'value', metric: 'm', key: 'route', partial: '', quoted: false } },
    { name: 'tırnak açılmış değer parçası', input: 'm{route="/ap|', want: { phase: 'value', metric: 'm', key: 'route', partial: '/ap', quoted: true } },
    { name: 'regex operatörü =~', input: 'm{route=~"a|', want: { phase: 'value', metric: 'm', key: 'route', partial: 'a', quoted: true } },
    { name: 'negatif regex !~', input: 'm{route!~"a|', want: { phase: 'value', metric: 'm', key: 'route', partial: 'a', quoted: true } },
    { name: 'eşit değil !=', input: 'm{route!="a|', want: { phase: 'value', metric: 'm', key: 'route', partial: 'a', quoted: true } },
    { name: 'anahtarın çevresindeki boşluk trim edilir', input: 'm{ route = "x|', want: { phase: 'value', metric: 'm', key: 'route', partial: 'x', quoted: true } },
    { name: 'sayıyla başlayan değer serbest', input: 'm{code="5|', want: { phase: 'value', metric: 'm', key: 'code', partial: '5', quoted: true } },

    // — tırnak farkındalığı: virgül/operatör/süslü değerin İÇİNDE —
    { name: 'tırnak içi virgül segmenti bölmez', input: 'm{a="x,y|', want: { phase: 'value', metric: 'm', key: 'a', partial: 'x,y', quoted: true } },
    { name: 'tırnak içi != operatör sanılmaz', input: 'm{a="x!=y|', want: { phase: 'value', metric: 'm', key: 'a', partial: 'x!=y', quoted: true } },
    { name: 'tırnak içi { süslü saymaz', input: 'm{route="/a{b|', want: { phase: 'value', metric: 'm', key: 'route', partial: '/a{b', quoted: true } },
    { name: 'kaçırılmış tırnak stringi kapatmaz', input: 'm{a="x\\"y|', want: { phase: 'value', metric: 'm', key: 'a', partial: 'x\\"y', quoted: true } },

    // — null: burası Faz 1'in işi —
    { name: 'kapanmış süslüden sonra → null (metrik moduna düşer)', input: 'm{a="x"} + rat|', want: null },
    { name: 'metriksiz çıplak selector → null', input: '{a|', want: null },
    { name: 'süslü hiç yok → null', input: 'rate(http.ser|', want: null },
    { name: 'süslüden hemen önce → null', input: 'metric|{a="b"}', want: null },
  ];

  for (const c of cases) {
    it(c.name, () => {
      const { text, pos } = cursor(c.input);
      const ctx = promqlLabelContext(text, pos);
      if (c.want === null) { expect(ctx).toBeNull(); return; }
      expect(ctx).not.toBeNull();
      expect(ctx!.phase).toBe(c.want.phase);
      expect(ctx!.metric).toBe(c.want.metric);
      expect(ctx!.partial).toBe(c.want.partial);
      if (c.want.key !== undefined) expect(ctx!.key).toBe(c.want.key);
      if (c.want.quoted !== undefined) expect(ctx!.quoted).toBe(c.want.quoted);
      // start/end MUST bracket the partial in the source text — applyLabel*
      // replaces exactly that range, so a wrong range corrupts the query.
      expect(text.slice(ctx!.start, ctx!.end)).toBe(ctx!.partial);
    });
  }

  it('Faz 1 ile örtüşmez: biri null derken diğeri cevap verir', () => {
    const inside = cursor('m{ser|');
    expect(promqlTokenAt(inside.text, inside.pos)).toBeNull();
    expect(promqlLabelContext(inside.text, inside.pos)?.phase).toBe('key');
    const outside = cursor('m{a="x"} + rat|');
    expect(promqlLabelContext(outside.text, outside.pos)).toBeNull();
    expect(promqlTokenAt(outside.text, outside.pos)?.text).toBe('rat');
  });

  it('aralık dışı konumlar null', () => {
    expect(promqlLabelContext('m{a', -1)).toBeNull();
    expect(promqlLabelContext('m{a', 9)).toBeNull();
  });
});

describe('applyLabelKey / applyLabelValue', () => {
  function ctxOf(marked: string): { text: string; ctx: PromqlLabelCtx } {
    const { text, pos } = cursor(marked);
    const ctx = promqlLabelContext(text, pos);
    if (!ctx) throw new Error(`no label context: ${marked}`);
    return { text, ctx };
  }

  it('anahtar: partial değişir, kuyruk korunur', () => {
    const { text, ctx } = ctxOf('m{htt|="x"}');
    const out = applyLabelKey(text, ctx, 'http.route');
    expect(out.text).toBe('m{http.route="x"}');
    expect(out.pos).toBe(12); // imleç anahtarın sonunda — operatörü operatör yazar
    expect(out.text.slice(0, out.pos)).toBe('m{http.route');
  });

  it('anahtar: boş süslüye ilk anahtarı yazar', () => {
    const { text, ctx } = ctxOf('m{|}');
    const out = applyLabelKey(text, ctx, 'service.name');
    expect(out.text).toBe('m{service.name}');
    expect(out.pos).toBe(14);
  });

  it('değer: tırnaksızken iki tırnak da eklenir, imleç kapanışın SONRASINDA', () => {
    const { text, ctx } = ctxOf('m{route=|');
    const out = applyLabelValue(text, ctx, '/api/orders');
    expect(out.text).toBe('m{route="/api/orders"');
    expect(out.pos).toBe(21);
    expect(out.text[out.pos - 1]).toBe('"');
  });

  it('değer: tırnak açıkken yalnız kapanış eklenir', () => {
    const { text, ctx } = ctxOf('m{route="/ap|');
    const out = applyLabelValue(text, ctx, '/api');
    expect(out.text).toBe('m{route="/api"');
    expect(out.pos).toBe(14);
    expect(out.text[out.pos - 1]).toBe('"');
  });

  it('değer: kapanış tırnağı zaten varsa çift yazmaz, üstünden atlar', () => {
    const { text, ctx } = ctxOf('m{route="/ap|"}');
    const out = applyLabelValue(text, ctx, '/api');
    expect(out.text).toBe('m{route="/api"}');
    expect(out.pos).toBe(14);
    expect(out.text.slice(out.pos)).toBe('}');
  });

  it('değer: regex operatöründen sonra da aynı tırnaklama', () => {
    const { text, ctx } = ctxOf('m{route=~"a|');
    const out = applyLabelValue(text, ctx, 'a.*');
    expect(out.text).toBe('m{route=~"a.*"');
    expect(out.pos).toBe(14);
  });

  it('değer: tırnak ve ters bölü kaçırılır', () => {
    const { text, ctx } = ctxOf('m{q=|');
    const out = applyLabelValue(text, ctx, 'a"b\\c');
    expect(out.text).toBe('m{q="a\\"b\\\\c"');
    expect(out.text[out.pos - 1]).toBe('"');
  });

  it('değer: virgülden sonraki ikinci matcher yerinde kalır', () => {
    const { text, ctx } = ctxOf('m{a="x",b="y|"}');
    const out = applyLabelValue(text, ctx, 'yes');
    expect(out.text).toBe('m{a="x",b="yes"}');
  });
});
