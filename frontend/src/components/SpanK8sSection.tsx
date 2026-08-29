// SpanK8sSection — v0.10.137 (DETAY SAYFALARI adım 3). Span detayının
// "Kubernetes" bölümü: cluster › namespace › pod › node zinciri, her biri
// entityHref ile (at = span başlangıcı → o an geçerli kayıt; range korunur;
// pod linki servis bağlamını taşır). Üç link-yok durumu açıkça ilan edilir
// (bağlam yok / cluster değeri yok / cluster eşlenmemiş). Gösterim kararı
// (bayrak + uygulama-içi link izni) çağıranda: SpanDetail Section'ı sarar.
import { Link } from 'react-router-dom';
import { Row, Badge } from '@/components/ui';
import { spanK8sContext, spanK8sNote } from '@/lib/spanK8s';
import type { EntityClusterInfo, SpanRow, TimeRange } from '@/lib/types';

export function SpanK8sSection({ span, clusters, range }: { span: SpanRow; clusters: EntityClusterInfo[]; range?: TimeRange | string | null }) {
  const ctx = spanK8sContext(span, clusters, range);
  const note = spanK8sNote(ctx);
  if (ctx.reason !== 'ok') {
    // v0.10.150 — 'no-namespace': pod linki yok ama cluster/node linkleri
    // VAR; onları da çiz, notu koru (eskiden hepsi düşüyordu).
    return (
      <Row gap={2} wrap>
        <Badge tone="neutral">{ctx.reason === 'no-k8s' ? 'bağlam yok' : 'link yok'}</Badge>
        {ctx.clusterHref && <Link to={ctx.clusterHref} className="sec" title={ctx.clusterId}>{ctx.clusterName}</Link>}
        {ctx.pod && <span className="mono">{ctx.pod}</span>}
        {ctx.nodeHref && <><span className="field-hint">on</span><Link to={ctx.nodeHref} className="sec" title="Node detayı">{ctx.node}</Link></>}
        <span className="field-hint">{note}</span>
      </Row>
    );
  }
  return (
    <Row gap={2} wrap>
      {ctx.clusterHref && <Link to={ctx.clusterHref} className="sec" title={ctx.clusterId}>{ctx.clusterName}</Link>}
      {ctx.namespaceHref && <><span className="field-hint">›</span><Link to={ctx.namespaceHref} className="sec">{ctx.namespace}</Link></>}
      <span className="field-hint">›</span>
      <Link to={ctx.podHref!} className="sec" title={`Pod detayı · ${ctx.clusterName} / ${ctx.namespace} / ${ctx.pod} · span anı`}>{ctx.pod}</Link>
      {ctx.nodeHref && <><span className="field-hint">on</span><Link to={ctx.nodeHref} className="sec" title="Node detayı">{ctx.node}</Link></>}
    </Row>
  );
}
