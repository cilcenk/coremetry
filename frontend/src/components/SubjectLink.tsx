import { Link } from 'react-router-dom';
import type { CSSProperties, ReactNode } from 'react';
import { subjectIsLinkable, subjectLabel, subjectTitle, subjectKind } from '../lib/problemSubject';

// SubjectLink — v0.9.1339 (entity-model Faz 4b).
//
// Bir problem/inbox satırının ÖZNESİNİ basar. Servis öznesinde bugüne
// dek olduğu gibi `/service?name=` linki; servis OLMAYAN öznede link
// YOK, okunabilir etiket + neden tıklanamadığını söyleyen title.
//
// Neden TEK bileşen: aynı `<Link to={serviceHref(p.service, …)}>` kalıbı
// altı dosyada tekrar ediyordu (ProblemsSection, Inbox, streams,
// InboxTriageDrawer, Shift, ProblemDetail). Sınıflandırmayı her birine
// ayrı yazmak, yedincisini yazan kişinin onu unutması demekti — ve
// unutulan yer sessizce çıkmaz link üretirdi, hata vermezdi.
//
// href'i BU bileşen kurmuyor: her çağrı yeri kendi range/tab/env'ini
// taşımaya devam etsin diye çağıran serviceHref'i kendisi çağırıp
// `href` olarak veriyor. Böylece v0.9.860'ın "satırın kendi olay
// penceresi linke biner" sözleşmesi olduğu gibi kalıyor.

// ⚠️ `subjectKind` prop'u BİLEREK ayrı ve BİLEREK bu adı taşıyor:
// `InboxItem.kind` zaten var ve satırın KAYNAĞINI söylüyor
// (problem|exception|anomaly). Tek bir `item` nesnesi alsaydık inbox
// çağrısı sessizce o alanı okurdu — iki alan da `string`, TypeScript
// susardı (identity.go'daki clusterExpr çakışmasının ikizi).
type Props = {
  /** Öznenin kendisi — Problem.service / InboxItem.service. */
  service: string;
  /** Öznenin TÜRÜ (Problem.kind / InboxItem.subjectKind). InboxItem.kind DEĞİL. */
  subjectKind?: string;
  /** Servis öznesi için hazır href (çağıran serviceHref ile kurar). */
  href: string;
  style?: CSSProperties;
  className?: string;
  onClick?: (e: React.MouseEvent) => void;
  /** Özne boşken basılacak içerik (Inbox'ın "(none)" hücresi). */
  emptyFallback?: ReactNode;
};

export function SubjectLink({ service, subjectKind: kind, href, style, className, onClick, emptyFallback }: Props) {
  const label = subjectLabel(service);

  if (!service && emptyFallback !== undefined) {
    return <span style={style} className={className}>{emptyFallback}</span>;
  }
  if (!subjectIsLinkable(service, kind)) {
    // Çalışmayan bir bağlantı, bağlantı olmamasından KÖTÜDÜR — aynı
    // gerekçe chstore selfHealthRunbooks yorumunda yazılı. Rozet, düz
    // metnin "bu satır bozuk" gibi okunmasını engelliyor.
    return (
      <span style={style} className={className} title={subjectTitle(service)}>
        {subjectKind(service, kind) === 'db' && (
          <span className="badge b-gray" style={{ fontSize: 9, marginRight: 4 }}>DB</span>
        )}
        {label}
      </span>
    );
  }
  return (
    <Link to={href} style={style} className={className} onClick={onClick}>
      {label}
    </Link>
  );
}
