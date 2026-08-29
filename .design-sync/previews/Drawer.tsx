import { Drawer, DrawerSection, DrawerTrendRow, Badge, Button, Field, Row, Stack, StatTile } from 'coremetry-ui';
import { Frame } from '../preview-lib/frame';

// Drawer portals to document.body (position:fixed, right edge): the card
// renders it open, full-bleed, no Frame — same precedent as Modal.

const REQ = [420, 398, 371, 352, 340, 336, 355, 402, 468, 551, 640, 712, 758, 781, 790, 774, 752, 735, 710, 668, 612, 560, 505, 462];
const P95 = [182, 176, 171, 169, 168, 170, 174, 183, 197, 214, 238, 262, 289, 301, 296, 288, 279, 271, 260, 249, 232, 215, 201, 190];
const ERR = [0.3, 0.2, 0.2, 0.3, 0.2, 0.2, 0.3, 0.4, 0.4, 0.5, 0.7, 1.1, 2.4, 3.8, 3.1, 1.9, 1.2, 0.8, 0.6, 0.5, 0.4, 0.3, 0.3, 0.3];

function Header({ name, badges }: { name: string; badges: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--sp-4)', minWidth: 0 }}>
      <span style={{ fontSize: 'var(--fs-lg)', fontWeight: 600, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{name}</span>
      {badges}
    </div>
  );
}

// Default: backdrop=true — the drawer INSPECTS one subject (a service host),
// the page behind is dimmed and inert. Width 560.
export function EntityDetail() {
  return (
    <Drawer onClose={() => {}}
      header={<Header name="payments-orchestrator" badges={<><Badge tone="danger">P1</Badge><Badge>prod-eu-west</Badge></>} />}>
      <DrawerSection title="Last 1h">
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, minmax(0, 1fr))', gap: 'var(--sp-4)' }}>
          <StatTile label="Requests">41.2k</StatTile>
          <StatTile label="p95" tone="warn">301 ms</StatTile>
          <StatTile label="Error rate" tone="err">3.8%</StatTile>
          <StatTile label="Pods">6 / 6</StatTile>
        </div>
      </DrawerSection>
      <DrawerSection title="Trends">
        <Stack gap={2}>
          <DrawerTrendRow label="Requests" values={REQ} color="var(--accent2)" />
          <DrawerTrendRow label="p95 ms" values={P95} color="var(--warn)" />
          <DrawerTrendRow label="Errors %" values={ERR} color="var(--err)" />
        </Stack>
      </DrawerSection>
      <DrawerSection title="Resource attributes">
        <table className="kv-table">
          <tbody>
            <tr><td>k8s.cluster.name</td><td>prod-eu-west</td></tr>
            <tr><td>k8s.namespace.name</td><td>payments</td></tr>
            <tr><td>k8s.deployment.name</td><td>payments-orchestrator</td></tr>
            <tr><td>service.version</td><td>2026.08.27-3</td></tr>
            <tr><td>telemetry.sdk.language</td><td>java</td></tr>
          </tbody>
        </table>
      </DrawerSection>
      <DrawerSection title="Actions">
        <Row gap={2} wrap>
          <Button variant="primary">Open traces</Button>
          <Button variant="secondary">Pin to dashboard</Button>
          <Button variant="accent" leftIcon={<span aria-hidden>✨</span>}>Explain</Button>
        </Row>
      </DrawerSection>
    </Drawer>
  );
}

// Narrow inspector (width 400) — an exception group with its stack head.
export function NarrowInspector() {
  return (
    <Drawer width={400} onClose={() => {}}
      header={<Header name="TimeoutException" badges={<Badge tone="danger">P1</Badge>} />}>
      <DrawerSection title="Group">
        <Stack gap={2}>
          <div style={{ fontSize: 'var(--fs-sm)', color: 'var(--text2)' }}>api-gateway → payments-orchestrator</div>
          <div className="mono" style={{ color: 'var(--text2)' }}>Read timed out after 5000 ms (POST /v1/charges)</div>
        </Stack>
      </DrawerSection>
      <DrawerSection title="Last 1h">
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, minmax(0, 1fr))', gap: 'var(--sp-4)' }}>
          <StatTile label="Occurrences" tone="err">1 284</StatTile>
          <StatTile label="Services">3</StatTile>
          <StatTile label="First seen">14:02</StatTile>
        </div>
      </DrawerSection>
      <DrawerSection title="Stack head">
        <pre style={{ fontSize: 'var(--fs-xs)', color: 'var(--text2)', whiteSpace: 'pre-wrap', lineHeight: 1.5 }}>
{`java.net.SocketTimeoutException: Read timed out
  at okhttp3.internal.http2.Http2Stream.waitForIo
  at okhttp3.internal.http2.Http2Stream.takeHeaders
  at com.coremetry.demo.charges.ChargeClient.post`}
        </pre>
      </DrawerSection>
      <Row gap={2}>
        <Button variant="secondary" size="sm">Resolve</Button>
        <Button variant="ghost" size="sm">Ignore group</Button>
      </Row>
    </Drawer>
  );
}

// backdrop=false — the CoSRE chat ACCOMPANIES the page instead of inspecting
// a subject: no scrim, the page behind stays scrollable/clickable, no
// click-outside-to-close. bodyStyle hands the panel a vertical flex layout
// (scrolling turn list + fixed composer).
export function CompanionChat() {
  return (
    <>
      <Frame style={{ minHeight: 560 }}>
        <div style={{ fontSize: 'var(--fs-xl)', fontWeight: 600, marginBottom: 'var(--sp-6)' }}>Traces</div>
        <table className="kv-table" style={{ maxWidth: 420 }}>
          <tbody>
            <tr><td>14:07:12</td><td>api-gateway · POST /v1/charges · 5.01 s · error</td></tr>
            <tr><td>14:07:11</td><td>api-gateway · POST /v1/charges · 5.00 s · error</td></tr>
            <tr><td>14:07:09</td><td>checkout-web · GET /cart · 84 ms</td></tr>
            <tr><td>14:07:08</td><td>api-gateway · POST /v1/charges · 4.98 s · error</td></tr>
          </tbody>
        </table>
      </Frame>
      <Drawer width={420} backdrop={false} onClose={() => {}}
        bodyStyle={{ display: 'flex', flexDirection: 'column', overflow: 'hidden' }}
        header={<Header name="CoSRE" badges={<Badge tone="accent">gemma4 · local</Badge>} />}>
        <div style={{ flex: 1, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 'var(--sp-5)', paddingBottom: 'var(--sp-5)' }}>
          <div style={{ alignSelf: 'flex-end', maxWidth: '85%', background: 'var(--bg3)', borderRadius: 8, padding: 'var(--sp-4) var(--sp-5)', fontSize: 'var(--fs-sm)' }}>
            Why are /v1/charges calls timing out since 14:02?
          </div>
          <div style={{ alignSelf: 'flex-start', maxWidth: '92%', background: 'var(--bg2)', borderRadius: 8, padding: 'var(--sp-4) var(--sp-5)', fontSize: 'var(--fs-sm)', lineHeight: 1.55 }}>
            <p style={{ margin: 0 }}>All 1 284 timeouts share one downstream: <b>payments-orchestrator</b> pod <span className="mono">7d9f-x2k4</span>, where p95 rose from 182 ms to 301 ms right after deploy <span className="mono">2026.08.27-3</span> (14:01).</p>
            <p style={{ margin: 'var(--sp-4) 0 0' }}>The other five pods are unaffected — a rollback of that one pod is the smallest safe action.</p>
          </div>
        </div>
        <div style={{ borderTop: '1px solid var(--border)', paddingTop: 'var(--sp-5)' }}>
          <Stack gap={2}>
            <Field label="Ask" placeholder="Show the slowest span of that pod…" />
            <Row gap={2} justify="end">
              <Button variant="ghost" size="sm">Clear</Button>
              <Button variant="primary" size="sm">Send</Button>
            </Row>
          </Stack>
        </div>
      </Drawer>
    </>
  );
}
