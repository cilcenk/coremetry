import { describe, it, expect } from 'vitest';
import { kqlLint } from './kqlLint';
import { mergeOperatorSuggestions } from './kqlFieldToken';

// v0.9.1216 — gönderim-öncesi KQL denetimi. Yalnız NESNEL kırıklar
// engellenir; geçerli her şey null döner (yanlış pozitif operatörü
// kilitler — en önemli sözleşme alttaki "geçerli" grubu).
describe('kqlLint', () => {
  const valid = [
    '',
    'timeout',
    'level:error',
    'level:error AND service:checkout',
    '"exact phrase" OR code:500',
    '(a OR b) AND NOT c',
    'msg:"quoted \\" escape"',
    'NOT level:debug',           // baştaki NOT geçerli (tekli operatör)
    'a AND (b OR c)',
  ];
  for (const q of valid) {
    it(`geçerli: ${JSON.stringify(q)}`, () => expect(kqlLint(q)).toBeNull());
  }

  const broken: [string, string][] = [
    ['level:"error', 'tırnak'],
    ['(a OR b', 'parantez'],
    ['a OR b)', 'parantezi'],
    ['level:error AND', 'operatör'],
    ['a OR', 'operatör'],
    ['NOT', 'operatör'],
    ['AND level:error', 'başlayamaz'],
  ];
  for (const [q, frag] of broken) {
    it(`kırık: ${JSON.stringify(q)}`, () => {
      expect(kqlLint(q)).toContain(frag);
    });
  }

  it('tırnak içindeki parantez sayılmaz', () => {
    expect(kqlLint('msg:"(unclosed"')).toBeNull();
  });
});

describe('mergeOperatorSuggestions', () => {
  it('önek eşleşince operatörler başa girer', () => {
    expect(mergeOperatorSuggestions(['annotation', 'android'], 'an'))
      .toEqual(['AND', 'annotation', 'android']);
    expect(mergeOperatorSuggestions(['order_id'], 'o')).toEqual(['OR', 'order_id']);
    expect(mergeOperatorSuggestions(['note'], 'no')).toEqual(['NOT', 'note']);
  });
  it('eşleşme yoksa liste aynen', () => {
    expect(mergeOperatorSuggestions(['level'], 'lev')).toEqual(['level']);
  });
  it('boş önek operatör önermez (boş kutuda gürültü)', () => {
    expect(mergeOperatorSuggestions(['level'], '')).toEqual(['level']);
  });
});
