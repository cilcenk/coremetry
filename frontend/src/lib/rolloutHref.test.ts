import { describe, it, expect } from 'vitest';
import { encodeRolloutParam, decodeRolloutParam } from './rolloutRow';

// rolloutHref.test.ts — v0.10.203 çekmece URL codec'i (audit §12 Faz 4).
describe('rollout param codec', () => {
  const id = { clusterId: 'c-2db91847', namespace: 'demo', workload: 'api-gateway', revision: 'api-gateway-mv4nq', startedAt: 1788119700000 };
  it('gidiş-dönüş', () => {
    expect(decodeRolloutParam(encodeRolloutParam(id))).toEqual(id);
  });
  it('özel karakterler (~ / % boşluk) kimliği bozmaz', () => {
    const odd = { ...id, workload: 'a~b c/d%e', namespace: '' };
    expect(decodeRolloutParam(encodeRolloutParam(odd))).toEqual(odd);
  });
  it('bozuk token → null', () => {
    for (const bad of [null, '', 'a|b', 'a|b|c|d|notanumber', 'a|b|c|d|0', '||||5', 'a|b|c|d|5|f']) {
      expect(decodeRolloutParam(bad)).toBeNull();
    }
  });
});
