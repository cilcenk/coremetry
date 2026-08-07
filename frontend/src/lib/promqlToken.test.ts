// promqlToken tests (v0.9.766) — PromQL metrik-adı autocomplete'in saf
// çekirdeği. Tablo-güdümlü: imleç "|" ile işaretlenir, testte kesilir.
// Kritik davranışlar: noktalı OTel adı TEK token, süslü parantez içi
// (label matcher) null (Faz 2'nin işi), sayıyla başlayan token null
// ([5m] gibi aralıklar metrik adı değildir).

import { describe, it, expect } from 'vitest';
import { promqlTokenAt, replaceToken } from './promqlToken';

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
