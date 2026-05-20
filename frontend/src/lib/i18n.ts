import { useEffect, useState } from 'react';
import { useBranding } from './branding';

// Tiny in-repo i18n. No external dependency — the SPA's
// branding settings carry a `language` field that picks one of
// the catalogs below. English is the source of truth + the
// default; Turkish covers the high-traffic surfaces (sidebar,
// login, common buttons, page titles, empty/error states).
//
// Catalog covers ~80% of what an operator sees during morning
// triage. Strings that aren't in the catalog fall back to the
// English key verbatim so a missing translation never renders
// blank.
export type Lang = 'en' | 'tr';

type Catalog = Record<string, string>;

const EN: Catalog = {
  // Sidebar group headings
  'navGroup.triage':      'Triage',
  'navGroup.services':    'Services',
  'navGroup.signals':     'Signals',
  'navGroup.workspaces':  'Workspaces',
  'navGroup.alerting':    'Alerting',
  'navGroup.system':      'System',
  'navGroup.management':  'Management',

  // Sidebar
  'nav.inbox':       'Inbox',
  'nav.incidents':   'Incidents',
  'nav.problems':    'Problems',
  'nav.anomalies':   'Anomalies',
  'nav.services':    'Services',
  'nav.databases':   'Databases',
  'nav.messaging':   'Messaging',
  'nav.traces':      'Traces',
  'nav.metrics':     'Metrics',
  'nav.logs':        'Logs',
  'nav.explore':     'Explore',
  'nav.notebook':    'Notebook',
  'nav.dashboards':  'Dashboards',
  'nav.profiling':   'Profiling',
  'nav.ai':          'AI insights',
  'nav.contracts':   'Contracts',
  'nav.alerts':      'Alerts',
  'nav.serviceMap':  'Service map',
  'nav.topology':    'Topology',
  'nav.deploys':     'Deploys',
  'nav.slos':        'SLOs',
  'nav.monitors':    'Monitors',
  'nav.system':      'System',
  'nav.cardinality': 'Cardinality',
  'nav.cluster':     'Cluster',
  'nav.catalog':     'Service catalog',
  'nav.audit':       'Audit log',
  'nav.sql':         'SQL playground',
  'nav.query':       'Query (DQL)',
  'nav.statusPage':  'Public Status Page',

  // Login
  'login.signIn':      'Sign in',
  'login.signingIn':   'Signing in…',
  'login.email':       'Email',
  'login.password':    'Password',
  'login.usernameOrEmail': 'Username or email',
  'login.signInWith':  'Sign in with',
  'login.orLocal':     'or sign in locally',
  'login.signInToContinue': 'Sign in to continue',
  'login.invalid':     'Invalid username or password',
  'login.invalidHint': 'If you pasted from a document, a hyphen-minus (-) may have been replaced with a similar character (–, —, hidden whitespace). Try typing the password by hand.',

  // Common buttons
  'btn.save':    'Save',
  'btn.cancel':  'Cancel',
  'btn.delete':  'Delete',
  'btn.reset':   'Reset',
  'btn.create':  'Create',
  'btn.edit':    'Edit',
  'btn.close':   'Close',
  'btn.refresh': 'Refresh',
  'btn.search':  'Search',

  // Status / states
  'state.loading':       'Loading…',
  'state.noData':        'No data',
  'state.failed':        'Failed to load',
  'state.tryWidening':   'Try widening the time range.',

  // User menu
  'user.signOut':         'Sign out',
  'user.changePassword':  'Change password',
  'user.manageUsers':     'Manage users',
  'user.settings':        'Settings',

  // Common labels
  'label.service':    'Service',
  'label.severity':   'Severity',
  'label.status':     'Status',
  'label.started':    'Started',
  'label.duration':   'Duration',
  'label.errorRate':  'Error rate',
  'label.latency':    'Latency',
  'label.count':      'Count',
  'label.value':      'Value',
  'label.threshold':  'Threshold',

  // Severity badges
  'sev.critical': 'CRITICAL',
  'sev.warning':  'WARNING',
  'sev.info':     'INFO',
  'sev.open':     'OPEN',
  'sev.resolved': 'RESOLVED',
};

const TR: Catalog = {
  // Sidebar group headings
  'navGroup.triage':     'Olay yönetimi',
  'navGroup.services':   'Servisler',
  'navGroup.signals':    'Sinyaller',
  'navGroup.workspaces': 'Çalışma alanları',
  'navGroup.alerting':   'Alarm yönetimi',
  'navGroup.system':     'Sistem',
  'navGroup.management': 'Yönetim',

  // Sidebar
  'nav.inbox':       'Gelen kutusu',
  'nav.incidents':   'Olaylar',
  'nav.problems':    'Sorunlar',
  'nav.anomalies':   'Anomaliler',
  'nav.services':    'Servisler',
  'nav.databases':   'Veritabanları',
  'nav.messaging':   'Mesajlaşma',
  'nav.traces':      'İzler',
  'nav.metrics':     'Metrikler',
  'nav.logs':        'Loglar',
  'nav.explore':     'Keşfet',
  'nav.notebook':    'Defter',
  'nav.dashboards':  'Panolar',
  'nav.profiling':   'Profilleme',
  'nav.ai':          'AI gözlem',
  'nav.contracts':   'Sözleşmeler',
  'nav.alerts':      'Alarmlar',
  'nav.serviceMap':  'Servis haritası',
  'nav.topology':    'Topoloji',
  'nav.deploys':     'Deploy\'lar',
  'nav.slos':        'SLO\'lar',
  'nav.monitors':    'Monitörler',
  'nav.system':      'Sistem',
  'nav.cardinality': 'Kardinalite',
  'nav.cluster':     'Küme',
  'nav.catalog':     'Servis kataloğu',
  'nav.audit':       'Denetim kaydı',
  'nav.sql':         'SQL editörü',
  'nav.query':       'Sorgu (DQL)',
  'nav.statusPage':  'Genel Durum Sayfası',

  // Login
  'login.signIn':      'Giriş yap',
  'login.signingIn':   'Giriş yapılıyor…',
  'login.email':       'E-posta',
  'login.password':    'Parola',
  'login.usernameOrEmail': 'Kullanıcı adı veya e-posta',
  'login.signInWith':  'Şununla giriş yap:',
  'login.orLocal':     'veya yerel hesapla giriş yap',
  'login.signInToContinue': 'Devam etmek için giriş yapın',
  'login.invalid':     'Geçersiz kullanıcı adı veya parola',
  'login.invalidHint': 'Parolayı bir dokümandan kopyaladıysanız tire (-) yerine başka bir karakter (–, —, gizli boşluk) yapışmış olabilir; tekrar elle yazıp deneyin.',

  // Common buttons
  'btn.save':    'Kaydet',
  'btn.cancel':  'İptal',
  'btn.delete':  'Sil',
  'btn.reset':   'Sıfırla',
  'btn.create':  'Oluştur',
  'btn.edit':    'Düzenle',
  'btn.close':   'Kapat',
  'btn.refresh': 'Yenile',
  'btn.search':  'Ara',

  // Status / states
  'state.loading':     'Yükleniyor…',
  'state.noData':      'Veri yok',
  'state.failed':      'Yüklenemedi',
  'state.tryWidening': 'Zaman aralığını genişletmeyi deneyin.',

  // User menu
  'user.signOut':        'Çıkış yap',
  'user.changePassword': 'Parolayı değiştir',
  'user.manageUsers':    'Kullanıcıları yönet',
  'user.settings':       'Ayarlar',

  // Common labels
  'label.service':    'Servis',
  'label.severity':   'Önem',
  'label.status':     'Durum',
  'label.started':    'Başladı',
  'label.duration':   'Süre',
  'label.errorRate':  'Hata oranı',
  'label.latency':    'Gecikme',
  'label.count':      'Adet',
  'label.value':      'Değer',
  'label.threshold':  'Eşik',

  // Severity badges
  'sev.critical': 'KRİTİK',
  'sev.warning':  'UYARI',
  'sev.info':     'BİLGİ',
  'sev.open':     'AÇIK',
  'sev.resolved': 'ÇÖZÜLDÜ',
};

const CATALOGS: Record<Lang, Catalog> = { en: EN, tr: TR };

// User-level language override. Stored in localStorage so it
// survives reloads without a server round trip and so unauthed
// pages (the public-trace viewer, the login screen) can also
// honour the picked language. When unset, the branding-level
// language wins — operators who never touch the picker get the
// org default they always had.
const USER_LANG_KEY = 'coremetry.lang';
const USER_LANG_EVENT = 'coremetry:lang-change';

function readUserLang(): Lang | null {
  if (typeof window === 'undefined') return null;
  try {
    const v = window.localStorage.getItem(USER_LANG_KEY);
    if (v === 'tr' || v === 'en') return v;
  } catch { /* private mode etc. — silent */ }
  return null;
}

// useUserLang subscribes to localStorage + the same-tab
// USER_LANG_EVENT. The storage listener fires on OTHER tabs;
// the custom event fires within the current tab where the
// picker was clicked, so both paths flow back to every
// useT consumer immediately.
export function useUserLang(): Lang | null {
  const [lang, setLang] = useState<Lang | null>(() => readUserLang());
  useEffect(() => {
    const handler = () => setLang(readUserLang());
    window.addEventListener(USER_LANG_EVENT, handler);
    window.addEventListener('storage', handler);
    return () => {
      window.removeEventListener(USER_LANG_EVENT, handler);
      window.removeEventListener('storage', handler);
    };
  }, []);
  return lang;
}

// setUserLang persists the choice + broadcasts the same-tab
// event so every useT consumer re-renders without a manual
// reload. Passing null clears the override (falls back to
// branding).
export function setUserLang(lang: Lang | null): void {
  if (typeof window === 'undefined') return;
  try {
    if (lang) window.localStorage.setItem(USER_LANG_KEY, lang);
    else window.localStorage.removeItem(USER_LANG_KEY);
    window.dispatchEvent(new Event(USER_LANG_EVENT));
  } catch { /* private mode etc. — silent */ }
}

// useT returns a translator scoped to the effective language.
// Priority: user-picked (localStorage) → branding default →
// English fallback. Hook so any of those layers changing
// (admin saves new branding → invalidateBranding fires; user
// clicks the picker → USER_LANG_EVENT fires) flows through
// every consumer without a manual reload.
export function useT(): (key: string) => string {
  const brand = useBranding();
  const userLang = useUserLang();
  const lang: Lang = userLang ?? (brand.language === 'tr' ? 'tr' : 'en');
  const cat = CATALOGS[lang];
  return (key: string) => cat[key] ?? EN[key] ?? key;
}

// Direct catalog access for non-component code paths (e.g. an
// imperative toast). Mirrors useT but reads the cached branding
// synchronously.
export function t(key: string, lang: Lang = 'en'): string {
  return CATALOGS[lang][key] ?? EN[key] ?? key;
}
