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
  // Sidebar
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
  'nav.alerts':      'Alerts',
  'nav.serviceMap':  'Service map',
  'nav.slos':        'SLOs',
  'nav.monitors':    'Monitors',
  'nav.system':      'System',
  'nav.cardinality': 'Cardinality',
  'nav.catalog':     'Service catalog',
  'nav.audit':       'Audit log',
  'nav.sql':         'SQL playground',
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
  'login.invalid':     'Invalid email or password',

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
  // Sidebar
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
  'nav.alerts':      'Alarmlar',
  'nav.serviceMap':  'Servis haritası',
  'nav.slos':        'SLO\'lar',
  'nav.monitors':    'Monitörler',
  'nav.system':      'Sistem',
  'nav.cardinality': 'Kardinalite',
  'nav.catalog':     'Servis kataloğu',
  'nav.audit':       'Denetim kaydı',
  'nav.sql':         'SQL editörü',
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
  'login.invalid':     'Geçersiz e-posta veya parola',

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

// useT returns a translator scoped to the current branding
// language. Hook so a language change (admin saves new
// branding → invalidateBranding fires → useBranding re-renders)
// flows through every consumer without a manual reload.
export function useT(): (key: string) => string {
  const brand = useBranding();
  const lang: Lang = brand.language === 'tr' ? 'tr' : 'en';
  const cat = CATALOGS[lang];
  return (key: string) => cat[key] ?? EN[key] ?? key;
}

// Direct catalog access for non-component code paths (e.g. an
// imperative toast). Mirrors useT but reads the cached branding
// synchronously.
export function t(key: string, lang: Lang = 'en'): string {
  return CATALOGS[lang][key] ?? EN[key] ?? key;
}
