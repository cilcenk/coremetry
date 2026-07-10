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

export { Card } from './Card';
export type { CardProps } from './Card';

export { Badge } from './Badge';
export type { BadgeProps, Tone } from './Badge';

export { Drawer, DrawerSection, DrawerTrendRow } from './Drawer';

export { Modal } from './Modal';
export type { ModalProps } from './Modal';

export { Tabs } from './Tabs';
export type { TabsProps, TabItem } from './Tabs';

export { Field, SelectField, TextareaField } from './Field';
export type { FieldProps, SelectFieldProps, TextareaFieldProps } from './Field';

export { Stack, Row } from './Stack';
export type { StackProps, RowProps } from './Stack';

export { VirtualList } from './VirtualList';
export type { VirtualListProps } from './VirtualList';
