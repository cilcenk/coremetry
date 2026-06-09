// Coremetry wordmark — the "Core" segment renders in the corporate
// red (--brand, #E30613) per operator request (v0.8.107). Custom
// appName brandings render verbatim — only the stock name gets the
// two-tone treatment.
export function Wordmark({ name = 'Coremetry' }: { name?: string }) {
  if (name !== 'Coremetry') return <>{name}</>;
  return (
    <>
      <span style={{ color: 'var(--brand)' }}>Core</span>metry
    </>
  );
}
