// topicHref.test.ts — v0.10.575. Saf kodek: kimlik üçlüsü, sekme, pencere.
import { describe, it, expect } from 'vitest';
import {
  MSG_TOPIC_DEFAULT_TAB, MSG_TOPIC_PATH, messagingTopicHref, parseTopicRef, parseTopicTab,
} from './topicHref';

describe('parseTopicTab', () => {
  it('bilinen değerler geçer', () => {
    expect(parseTopicTab('consumers')).toBe('consumers');
    expect(parseTopicTab('clients')).toBe('clients');
    expect(parseTopicTab('spannames')).toBe('spannames');
  });
  it('boş / bilinmeyen değer VARSAYILANA düşer (sessiz boş gövde yok)', () => {
    expect(parseTopicTab(null)).toBe(MSG_TOPIC_DEFAULT_TAB);
    expect(parseTopicTab('')).toBe(MSG_TOPIC_DEFAULT_TAB);
    expect(parseTopicTab('kafka')).toBe(MSG_TOPIC_DEFAULT_TAB);
  });
});

describe('parseTopicRef', () => {
  it('üçlü tam ise ref döner; `?` önekli de okunur', () => {
    expect(parseTopicRef('system=kafka&cluster=(default)&destination=orders'))
      .toEqual({ system: 'kafka', cluster: '(default)', destination: 'orders' });
    expect(parseTopicRef('?system=kafka&cluster=c1&destination=a|b'))
      .toEqual({ system: 'kafka', cluster: 'c1', destination: 'a|b' });
  });
  it('eksik alan → null (cluster VARSAYILMAZ, v0.9.973)', () => {
    expect(parseTopicRef('system=kafka&destination=orders')).toBeNull();
    expect(parseTopicRef('system=kafka&cluster=&destination=orders')).toBeNull();
    expect(parseTopicRef('')).toBeNull();
  });
});

describe('messagingTopicHref', () => {
  it('kimlik + pencere taşır; varsayılan sekme YAZILMAZ', () => {
    const href = messagingTopicHref({
      system: 'kafka', cluster: '(default)', destination: 'orders', range: { preset: '6h' },
    });
    expect(href.startsWith(`${MSG_TOPIC_PATH}?`)).toBe(true);
    const sp = new URLSearchParams(href.slice(href.indexOf('?') + 1));
    expect(sp.get('system')).toBe('kafka');
    expect(sp.get('cluster')).toBe('(default)');
    expect(sp.get('destination')).toBe('orders');
    expect(sp.get('range')).toBe('6h');
    expect(sp.has('tab')).toBe(false);
    expect(messagingTopicHref({
      system: 'kafka', cluster: 'c', destination: 'o', tab: 'producers',
    })).not.toContain('tab=');
  });
  it('sekme verilirse yazılır; custom pencere token olarak geçer', () => {
    const href = messagingTopicHref({
      system: 'kafka', cluster: 'c', destination: 'o', tab: 'clients',
      range: { preset: 'custom', fromMs: 1000, toMs: 2000 },
    });
    const sp = new URLSearchParams(href.slice(href.indexOf('?') + 1));
    expect(sp.get('tab')).toBe('clients');
    expect(sp.get('range')).toBe('custom:1000-2000');
  });
  it('destination özel karakterleri kodlanır (alan sınırı uydurulamaz)', () => {
    const href = messagingTopicHref({ system: 'kafka', cluster: 'c', destination: 'a b&c=d' });
    expect(href).toContain('destination=a+b%26c%3Dd');
    expect(parseTopicRef(href.slice(href.indexOf('?') + 1))?.destination).toBe('a b&c=d');
  });
});
