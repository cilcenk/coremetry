# Coremetry UI — build conventions for the design agent

Coremetry is an OpenTelemetry-native APM (services, traces, logs, problems,
pods/clusters). The UI is **dark-first, dense, operator-grade** — think
Datadog / Dynatrace density with Red Hat / PatternFly restraint. Every
screen you build must look like one of its pages.

## Setup and wrapping

- No theme provider. Styling comes entirely from `styles.css` (the bound
  copy of `globals.css`): CSS custom properties on `:root` (dark default)
  and token remaps under `[data-theme="light"]` / `[data-theme="redhat"]`.
  Put `data-theme="light"` on a root element only when the user asks for
  light mode; otherwise design on the dark palette.
- The page ground is `var(--bg)`, text `var(--text)`. Always paint the root
  of a design with `background: var(--bg); color: var(--text)` — the
  components assume that ground and their contrast is tuned for it.
- The only provider in the library is `ConfirmProvider`: wrap the app root
  in it when any component calls `useConfirm()` (destructive confirmations).
  Without it, `useConfirm` throws.
- Drawer and Modal portal to `document.body`; render them conditionally
  (`open` / mount) — they own their backdrop and z-index (`--z-modal`,
  `--z-drawer`).

## Styling idiom — tokens, never literals

Never write hex/rgb colors, px font sizes, or px radii. Use the tokens:

| Family | Real names |
|---|---|
| Surfaces | `--bg0` (page splash) · `--bg` (page) · `--bg1` (raised panel/card) · `--bg2` (inputs, hover) · `--bg3` |
| Text | `--text` · `--text2` (secondary) · `--text3` (muted/labels) · `--text-faint` |
| Lines | `--border` · `--border-strong` |
| Semantic | `--ok` · `--warn` · `--err` · `--info` · `--accent` (primary action/link) · `--accent2` · `--brand` |
| Series | `--purple` `--indigo` `--orange` `--teal` (chart lines only) |
| Spacing | `--sp-1` (2px) … `--sp-4` (8px) … `--sp-8` (24px) |
| Type scale | `--fs-3xs` `--fs-2xs` `--fs-xs` (11px) `--fs-sm` `--fs-md` (13px body) `--fs-lg` `--fs-xl` |
| Radius | `--radius-xs` `--radius-sm` `--radius` `--radius-lg` |
| Shadows | `--shadow-sm` `--shadow-md` `--shadow-pop` `--shadow-modal` |
| Layers | `--z-nav` `--z-dropdown` `--z-popover` `--z-drawer` `--z-modal` `--z-toast` |

Shared classes you may use for glue (all defined in `styles.css`):
`.badge` with one tone class `.b-ok` `.b-warn` `.b-err` `.b-info` `.b-gray`
(status pills; prefer the `Badge` component), `.mono` (ids, pod names, SQL),
`.sec` (secondary link-styled text/chips), `.tab-strip` (tab row),
`.table-wrap` (horizontal-scroll container for wide tables), `.card`.
Do not invent class names — anything not listed here or in a component's
`.prompt.md` does not exist.

## Component vocabulary

- `Button` — `variant` is REQUIRED: `primary` (one per view), `secondary`,
  `accent` (AI / highlighted), `ghost`, `danger`, `ghost-danger`; `size`
  `xs|sm|md|lg`; `loading`, `leftIcon`/`rightIcon`. `IconButton` needs
  `aria-label` + `icon`. `LinkButton` = link-looking button (no navigation).
- `Badge` (`tone`: neutral|info|success|warning|danger|accent), `Chip`
  (filter/toggle, `active`, `pill`), `DisclosureButton` (`expanded`,
  `anatomy` row|section) for collapsible sections.
- Forms: `Field`, `SelectField`, `TextareaField` (`label`, `hint`, `error`),
  `SearchField` (controlled `value`/`onChange(string)`), `ActionRow`
  (`destructive` | `secondary` | `confirm` slots — one confirm only).
- Layout: `PageShell` (page frame), `PageControls` (filter/action bar),
  `Card` (`header`, `footer`, `density`), `Stack`/`Row` (`gap` 1|2|3|4|6,
  `Row` `justify`/`wrap`/`grow`), `PanelTitle` (`sub`, `right`), `StatTile`
  (label + value; `tone` err|warn colours the value).
- Overlays/lists: `Modal` (`open`, `title`, `footer`, `size`), `Drawer`
  (+ `DrawerSection`, `DrawerTrendRow`), `MenuItem`, `FacetMultiSelect`,
  `VirtualList` / `VirtualTable` for > 100 rows.

## Where the truth lives

Read `styles.css` for tokens and shared classes, and each
`components/general/<Name>/<Name>.prompt.md` + `<Name>.d.ts` for the exact
props before using a component. Preview cards show canonical compositions.

## Idiomatic snippet

```tsx
const { Card, Row, Stack, Badge, Button, StatTile } = window.CoremetryUI;

<div style={{ background: 'var(--bg)', color: 'var(--text)', padding: 'var(--sp-6)' }}>
  <Card header={<Row justify="between"><span>payments-orchestrator</span><Badge tone="danger">P1</Badge></Row>}>
    <Stack gap={3}>
      <Row gap={3}>
        <StatTile label="Error rate" tone="err">4.2%</StatTile>
        <StatTile label="P95 latency">812 ms</StatTile>
      </Row>
      <Row gap={2} justify="end">
        <Button variant="secondary" size="sm">Acknowledge</Button>
        <Button variant="accent" size="sm">Explain root cause</Button>
      </Row>
    </Stack>
  </Card>
</div>
```
