// Barrel export for the design-system primitives. Page / feature
// components should import from `@/components/ui` rather than
// reaching into individual files — keeps the surface stable when
// a primitive's internal file split changes.
//
// Why a barrel here but not elsewhere? The ui/ dir is the closed
// design-system boundary. Other component dirs are loose
// collections that should be imported directly so unused ones
// don't drag dependencies into the bundle.

export { Button } from './Button';
export type { ButtonProps } from './Button';

export { IconButton } from './IconButton';
export type { IconButtonProps } from './IconButton';

export { LinkButton } from './LinkButton';
export type { LinkButtonProps } from './LinkButton';

export { MenuItem } from './Menu';
export type { MenuItemProps } from './Menu';

export { Chip } from './Chip';
export type { ChipProps } from './Chip';

export { DisclosureButton } from './DisclosureButton';
export type { DisclosureButtonProps } from './DisclosureButton';

export { Card } from './Card';
export type { CardProps } from './Card';

export { Badge } from './Badge';
export type { BadgeProps, Tone } from './Badge';

export { Drawer, DrawerSection, DrawerTrendRow } from './Drawer';

export { Modal } from './Modal';
export type { ModalProps } from './Modal';

export { ConfirmProvider, useConfirm } from './ConfirmDialog';
export type { ConfirmOptions } from './ConfirmDialog';

export { SearchField } from './SearchField';
export type { SearchFieldProps } from './SearchField';

export { ActionRow } from './ActionRow';
export type { ActionRowProps } from './ActionRow';

// Tabs — REMOVED v0.9.904. A typed `.tab-strip` wrapper existed here
// from the design-system push but never gained a single consumer: all
// 11 `.tab-strip` surfaces write the plain `<div className="tab-strip">`
// markup directly. Rather than keep an unused primitive alive (and keep
// paying for its a11y semantics in review — see v0.9.900's dangling
// `aria-controls`), the file is gone. The CSS pattern itself IS the
// contract: `.tab-strip` in globals.css stays and is what pages target.

export { Field, SelectField, TextareaField } from './Field';
export type { FieldProps, SelectFieldProps, TextareaFieldProps } from './Field';

export { Stack, Row } from './Stack';
export type { StackProps, RowProps } from './Stack';

export { VirtualList } from './VirtualList';
export type { VirtualListProps } from './VirtualList';

// mK7 (v0.9.921) — dört primitif barrel'da EKSİKTİ. Çağrı yerleri
// bugün dosyaya doğrudan uzanıyor, yani "ui/ kapalı tasarım-sistemi
// sınırı" kuralı bu dördü için fiilen uygulanmıyordu. Ekleme SALT
// EKLEME: mevcut doğrudan import'lar taşınmıyor (14 dosyalık bir
// süpürme, kendi kararı), ama yeni çağrı yeri artık barrel'ı bulur.
export { VirtualTable } from './VirtualTable';
export { FacetMultiSelect } from './FacetMultiSelect';
export { PageControls } from './PageControls';
export { RouteSkeleton } from './RouteSkeleton';

// v0.9.1300 — BEŞİNCİ eksik primitif, aynı mK7 gerekçesiyle. PageShell
// kullanımda ikinci sıradaki primitif (59 site / 44 dosya) ama barrel'da
// YOKTU: "bu zaten var mı" diye barrel'a bakan biri onu bulamıyordu.
// Bu, ölçülen kopya ailelerinin (Stat ×6, Field ×7) doğum mekanizmasının
// ta kendisi — /frontend-design-system §1 "barrel YETMEZ" uyarısı bunun
// üzerine yazıldı. Salt EKLEME: mevcut doğrudan import'lar taşınmıyor.
export { PageShell } from './PageShell';

// v0.9.1364 — bölüm-içi yokluk satırı. İki BAYT BAYT aynı yerel nüshanın
// (endpoints/detailSections + slowqueries/StmtDetailDrawer) tek eve
// çekilmişi; `sectionAtomsGate.test.ts` üçüncüsünün doğmasını engelliyor.
export { SectionUnavailable } from './SectionUnavailable';

// v0.9.1365 — kart başlığı + dürüstlük yuvası. İki nüsha BAYT BAYT DEĞİLDİ
// (sayfa kopyası `right` yuvasını kaybetmişti); terfi `right`lı sürüme.
export { PanelTitle } from './PanelTitle';
