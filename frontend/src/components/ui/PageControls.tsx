import { Children, isValidElement, useEffect, useId, useMemo, useRef, useState } from 'react';
import type { CSSProperties, HTMLAttributes, ReactNode } from 'react';
import { useIsNarrow } from '@/lib/useNarrow';
import { useEscLayer } from '@/lib/escLayer';
import { DisclosureButton } from './DisclosureButton';

// v0.9.639 — liste sayfalarının filtre barı için tek primitif.
//
// Denetim bulgusu #5 (etki 5/5): "Hiçbir liste sayfasında sticky filtre
// barı yok — filtreler kaydırınca ekrandan çıkıyor." Uzun bir tabloda
// operatör filtreyi değiştirmek için her seferinde başa dönüyor.
//
// SAYFA KABUĞU ZATEN DOĞRU — bunu ölçtükten sonra kapsamı daralttım:
// `#app` 100vh/overflow:hidden, `#topbar` flex-shrink:0, `#content`
// flex:1+overflow:auto. Yani "sayfa başına tek scroll ekseni" ve
// "listeye max-width yok" hedefleri ZATEN sağlanıyor. Eksik olan tek
// şey barın yapışkanlığıydı; genel bir "PageShell" yazmak var olan
// doğru kabuğu gereksiz yere yeniden icat etmek olurdu.
//
// Yapışkanlık OPT-IN (`sticky` prop'u): `.controls` sınıfını 30 dosya
// kullanıyor ve hepsi liste sayfası değil — kimi kart ızgarası, kimi
// detay paneli. Görsel olarak doğrulanmamış bir davranış değişikliği
// 30 sayfaya birden yayılmaz (artımsal rollout).
//
// İÇ ÇERÇEVE KURMUYOR: bar `#content`'in kendi kaydırma eksenine
// yapışıyor, yeni bir kaydırma konteyneri açmıyor. `.table-wrap`'ın
// yaptığı hata tam buydu ve sticky tablo BAŞLIĞINI da o öldürüyor
// (ayrı dilim — yatay kaydırma davranışını değiştiriyor).

export interface PageControlsProps {
  children: ReactNode;
  /** Kaydırırken üstte kalsın mı. Liste sayfalarında true. */
  sticky?: boolean;
  className?: string;
  /** Sayfa-özel ince ayar (tipik: marginBottom). Dönüştürülen
      sayfaların taşıdığı mevcut değerleri kaybetmemek için var —
      yeni kullanımda gerekmiyor. */
  style?: CSSProperties;
}

// v0.9.669 — kalan div ÖZNİTELİKLERİ geçirgen.
//
// AIObservability'nin barı `data-shortcut-search` taşıyordu ve
// GlobalShortcuts o özniteliği arama kısayolunun AÇIK işareti olarak
// arıyor (querySelectorAll('[data-shortcut-search]')). PageControls'a
// dönüştürürken öznitelik sessizce düşmüştü — sayfa çalışmaya devam
// eder, yalnız kısayol ölür; fark edilmesi zor bir kayıp.
//
// Genel geçirgenlik tercih edildi çünkü sorun tek bir öznitelikte
// değil: `.controls`u kullanan 20+ sayfanın taşıdığı her data-*/aria-*
// aynı şekilde düşerdi.
type PageControlsAllProps = PageControlsProps &
  Omit<HTMLAttributes<HTMLDivElement>, 'style' | 'className' | 'children'>;

// ── D6.4 (v0.9.1001) — ≤640px'te KATLANIR bar ──────────────────────────
//
// Sorun ölçüldü: 390px'lik bir telefonda bu barlar üç-dört satıra
// sarıyor (Services 9 kontrol, Traces 12+). Yapışkan olduğu için o
// yükseklik ekranın üstünde SABİT duruyor — tablo için ~%40 yer kalıyor,
// ve `--controls-h` üzerinden yapışkan tablo başlığı da o kadar aşağı
// itiliyor. Yani düzeltilen bir kusur (bar kaybolmasın) dar ekranda
// ikinci bir kusura dönüşüyordu (bardan başka bir şey görünmesin).
//
// KATLAMA SALT GÖRÜNÜMDÜR: filtrelerin kendisine, URL yazımına,
// state'e, sıraya DOKUNULMUYOR. Çocuklar HER ZAMAN mount kalıyor —
// panel kapalıyken `display: none`. Şart koşulan iki şey bunu
// gerektirdi: (1) kapalıyken de doğru sayabilmek (aşağıda), (2) bir
// çocuğun yerel state'inin (picker taslağı, açık popover) panel her
// kapandığında sıfırlanmaması.
//
// MASAÜSTÜNDE DOM BİREBİR AYNI: geniş ekranda hiçbir sarmalayıcı,
// hiçbir düğme basılmıyor — aşağıdaki `narrow` dalı tek `<div
// className="controls">`e düşüyor. Eşik `useNarrow`'dan geliyor
// (`NARROW_MAX_PX = 640`), yani CSS telefon katmanıyla AYNI sayı;
// ayrışmayı `styles/mobileBreakpoints.test.ts` çiviliyor.

// Ana yüzeyde kalacak çocuğu seçen SAF kural. Dışa açık: tablo testi
// `pageControlsCollapse.test.tsx` bunu doğrudan sürüyor.
//
// KURAL: ilk ETKİLEŞİMLİ çocuk. Ölçüldü (14 tüketici, v0.9.1001) —
// 9'unda o çocuk zaten arama/serbest-metin girdisi ya da picker
// (Services/Endpoints/Logs/Anomalies/Traces `ServicePicker`,
// AIObservability/Dashboards/Metrics metin kutusu), kalan 5'inde
// sayfanın ilk gerçek kontrolü (zaman aralığı select'i, "+ New user").
//
// "İlk METİN KUTUSUNU ara, nerede olursa olsun" kuralı DENENDİ ve
// bırakıldı: Traces'ta o kutu `Trace ID…`, yani barın en sonundaki
// nokta-atışı alanı — arama kutusu değil. Sırayı bozan bir kural,
// sırayı koruyan bir kuraldan daha çok şaşırtıyor.
const HOST_CONTROLS = new Set(['input', 'select', 'textarea', 'button', 'label']);

export function isInteractiveChild(node: ReactNode): boolean {
  if (!isValidElement(node)) return false;            // metin / sayı düğümü
  const t = node.type;
  // Host eleman: yalnız gerçek kontroller. `<div className="segmented">`
  // ve bilgilendirme `<span>`leri ELENİR — birincisi bir kap, ikincisi
  // tıklanamaz metin.
  if (typeof t === 'string') return HOST_CONTROLS.has(t);
  // Bileşen (picker, Button, Link…). `typeof` ile bakılıyor, ADLA
  // değil: prod derlemesinde fonksiyon adları küçültücüde değişebilir
  // ve ada dayanan bir kural yalnız üretimde sessizce yanlış davranırdı.
  if (typeof t === 'function') return true;
  // memo/forwardRef sarmalı bileşen ({$$typeof, …}) — o da bileşen.
  // Fragment `symbol` olduğu için buraya DÜŞMEZ ve elenir: `<>…</>`
  // koşullu bir GRUP, tek bir kontrol değil.
  return typeof t === 'object' && t !== null;
}

// v0.9.1004 (etkileşim denetimi M2/O5) — AÇIK lead işareti.
//
// Yukarıdaki heuristik "ilk ETKİLEŞİMLİ çocuk" diyor ve 14 tüketicinin
// 9'unda o çocuk gerçekten bir arama ya da picker. Ama PICKER ≠ SAYFANIN
// ARAMASI: `/logs`ta ilk etkileşimli çocuk ServicePicker, sayfanın
// varlık nedeni olan KQL kutusu ise ONUN ARKASINDA — telefonda operatör
// /logs'u açıp aramak için önce "Filtreler" popover'ını açmak zorunda
// kalıyordu. Aynı şey `/anomalies` (Search type/message) ve daha hafif
// biçimde `/endpoints` (Filter by path) için de geçerliydi.
//
// NEDEN SAYISAL `leadIndex` DEĞİL: çocuklar koşullu.
// `{clusters.length > 0 && <select/>}` veri gelene kadar hiç render
// edilmiyor ve `Children.toArray` false'u ELİYOR — yani doğru indeks
// veriye göre kayardı. Logs'ta o koşullu select tam da KQL kutusundan
// ÖNCE duruyor, yani sabit bir sayı boş kümede yanlış çocuğu seçerdi.
// İşaret çocuğun kendisine yapışıyor, sırasına değil.
//
// İşaret ELEMENT PROPS'undan okunuyor, DOM'dan değil: lead seçimi
// render'dan ÖNCE yapılıyor, o anda DOM yok.
export function hasLeadMark(node: ReactNode): boolean {
  if (!isValidElement(node)) return false;
  const props = node.props as Record<string, unknown> | null | undefined;
  if (!props) return false;
  const v = props['data-pc-lead'];
  return v !== undefined && v !== false && v !== null;
}

export function leadChildIndex(items: ReactNode[]): number {
  // İşaretli çocuk KAZANIR. İşaret yoksa davranış bit-bit eski hâl —
  // geriye uyumlu, 14 tüketicinin 11'i tek satır bile değişmeden kalıyor.
  for (let i = 0; i < items.length; i++) if (hasLeadMark(items[i])) return i;
  for (let i = 0; i < items.length; i++) if (isInteractiveChild(items[i])) return i;
  return items.length > 0 ? 0 : -1;   // hiç kontrol yoksa ilk çocuk
}

export function PageControls({ children, sticky = false, className, style, ...rest }: PageControlsAllProps) {
  const ref = useRef<HTMLDivElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const panelId = useId();
  const narrow = useIsNarrow();
  const [open, setOpen] = useState(false);
  // Katlananlar arasındaki GERÇEK form kontrolü sayısı — DOM'dan.
  const [hiddenControls, setHiddenControls] = useState(0);

  const items = useMemo(() => Children.toArray(children), [children]);
  const lead = leadChildIndex(items);

  // v0.9.644 — barın YÜKSEKLİĞİNİ `--controls-h` olarak yayınla.
  //
  // Yapışkan tablo BAŞLIĞI da `#content`'e yapışıyor; ikisi de top:0
  // olsaydı üst üste binerlerdi. Başlık `top: var(--controls-h)` ile
  // barın ALTINA yapışıyor.
  //
  // Sabit bir sayı yazmak kırılgan olurdu: bar sarınca (dar pencere,
  // çok filtre) yüksekliği değişiyor. ResizeObserver bunu izliyor.
  //
  // Değişken `#content`'e yazılıyor — sayfa başına tek scrollport o, ve
  // aynı ağaçtaki tablolar oradan miras alıyor. Bar yoksa değişken hiç
  // tanımlanmıyor ve başlık `top: 0` fallback'ine düşüyor.
  useEffect(() => {
    const el = ref.current;
    if (!el || !sticky) return;
    // v0.9.981 — `.page-body` de geçerli host: `/problems` ve `/inbox`
    // detay açıkken liste kabı id yerine sınıf taşıyor (çift `#content`
    // düzeltmesi). Yalnız `#content` aransaydı o iki sayfada
    // `--controls-h` HİÇ yayınlanmaz, yapışkan tablo başlığı `top: 0`
    // fallback'ine düşüp filtre barının ALTINA gizlenirdi.
    const host = el.closest('#content, .page-body') as HTMLElement | null;
    if (!host) return;
    const apply = () => host.style.setProperty('--controls-h', `${el.offsetHeight}px`);
    apply();
    const ro = new ResizeObserver(apply);
    ro.observe(el);
    return () => {
      ro.disconnect();
      host.style.removeProperty('--controls-h');
    };
  }, [sticky]);

  // SAYAÇ — YALAN SAYAÇ YASAK.
  //
  // Sayılan şey: katlananların içindeki `input/select/textarea`, yani
  // panel açıldığında operatörün gerçekten göreceği FİLTRE ALANLARI.
  // Bilgilendirme `<span>`leri, "← Database overview" linkleri ve eylem
  // butonları sayılmıyor.
  //
  // Neden "AKTİF filtre" sayılmıyor: çocuklar bu bileşen için opak
  // `ReactNode`. Aktifliği tahmin etmenin tek yolu heuristik olurdu
  // (`value !== ''`, `select` ilk seçenekte mi…) ve bu depoda `select`
  // varsayılanları "All clusters"/"All db names" gibi BOŞ DEĞERLER —
  // "aktif" ile "varsayılan"ı ayırt etmenin kaynak-seviyesi yolu yok.
  // Yanlış bir aktiflik sayacı, sayaç olmamasından kötüdür: operatör
  // "3 filtre açık" okuyup filtresiz bir listeye bakar. Bu yüzden sayı
  // "arkada N alan var" diyor ve düğmenin `title`ı bunu birebir yazıyor.
  //
  // DOM'dan sayılıyor çünkü kaynak sayımı da yalan söylerdi: bir çocuk
  // (`{cond && <select/>}`) veri gelene kadar HİÇ render edilmiyor —
  // ör. Endpoints'in cluster select'i. MutationObserver o gecikmeli
  // gelişleri de yakalıyor.
  useEffect(() => {
    const el = panelRef.current;
    if (!narrow || !el) { setHiddenControls(0); return; }
    const count = () => setHiddenControls(el.querySelectorAll('input, select, textarea').length);
    count();
    const mo = new MutationObserver(count);
    mo.observe(el, { childList: true, subtree: true });
    return () => mo.disconnect();
  }, [narrow]);

  // Dışarı tık. Dinleyici yalnız açıkken bağlı.
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    return () => document.removeEventListener('mousedown', onDown);
  }, [open]);

  // Ekran genişlerse (cihaz döndürme, masaüstü pencere) katlama YOK
  // olur; açık kalan state bir sonraki daralmada paneli kendiliğinden
  // açık gösterirdi.
  useEffect(() => { if (!narrow) setOpen(false); }, [narrow]);

  // Esc KATMANI (E2/Ö28 disiplini): bar bir çekmecenin içindeyse ilk
  // Esc paneli kapatır, çekmeceyi değil. Kayıt AÇIKLIĞA bağlı — kapalı
  // bir panel yığında durup Esc'i yutmamalı.
  useEscLayer(open, () => setOpen(false));

  const cls = [
    'controls',
    sticky ? 'is-sticky' : '',
    narrow ? 'is-collapsible' : '',
    className ?? '',
  ].filter(Boolean).join(' ');

  // Masaüstü dalı: v0.9.1000'e kadarki DOM'un BİREBİR aynısı.
  if (!narrow) {
    return (
      // rest ÖNCE: className/style/ref açıkça sonra yazılıyor ki bir
      // çağıran onları kazara ezemesin.
      <div {...rest} ref={ref} className={cls} style={style}>
        {children}
      </div>
    );
  }

  const restItems = items.filter((_, i) => i !== lead);

  return (
    <div {...rest} ref={ref} className={cls} style={style}>
      {lead >= 0 && items[lead]}
      {restItems.length > 0 && (
        <DisclosureButton
          anatomy="row"
          className="pc-more"
          expanded={open}
          aria-controls={panelId}
          title={hiddenControls > 0
            ? `${hiddenControls} filtre alanı bu düğmenin arkasında`
            : 'Kalan kontroller bu düğmenin arkasında'}
          onClick={() => setOpen(o => !o)}>
          Filtreler{hiddenControls > 0 ? ` (${hiddenControls})` : ''}
        </DisclosureButton>
      )}
      {/* Panel HER ZAMAN basılıyor (kapalıyken `display: none`):
          çocuklar mount kalsın ve sayaç kapalıyken de doğru olsun.
          Sıra korunuyor — filtreler barda hangi sırayla duruyorsa
          panelde de o sırayla dikey diziliyor. */}
      <div ref={panelRef} id={panelId} role="group" aria-label="Filtreler"
        className={open ? 'pc-panel is-open' : 'pc-panel'}>
        {restItems}
      </div>
    </div>
  );
}
