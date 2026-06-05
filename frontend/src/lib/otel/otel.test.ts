import { describe, it, expect } from 'vitest';
import { resolveResource, attrFamily, isResourceAttrKey, normSpanKind, spanStatus } from './semconv';
import { extractSpanLinks, spanExceptions, spanHasError } from './links';
import type { SpanRow } from '@/lib/types';

describe('resolveResource', () => {
  it('extracts the critical-5 + k8s/cloud/runtime with coalesce', () => {
    const r = resolveResource({
      'service.name': 'transfer-service',
      'service.version': '1.0.0',
      'deployment.environment': 'prod',
      'openshift.cluster.name': 'ocp-east', // coalesced cluster
      'k8s.pod.name': 'transfer-7f-abc',
      'cloud.region': 'eu-west-1',
      'process.runtime.name': 'OpenJDK',
    });
    expect(r.serviceName).toBe('transfer-service');
    expect(r.cluster).toBe('ocp-east');
    expect(r.k8s.pod).toBe('transfer-7f-abc');
    expect(r.cloud.region).toBe('eu-west-1');
    expect(r.runtime.name).toBe('OpenJDK');
    // host.name + service.instance.id absent → flagged.
    expect(r.missingCritical).toContain('host.name');
    expect(r.missingCritical).toContain('service.instance.id');
    expect(r.missingCritical).not.toContain('service.name');
  });
});

describe('attrFamily / isResourceAttrKey', () => {
  it('classifies by semconv prefix (longest-first)', () => {
    expect(attrFamily('process.runtime.name')).toBe('runtime');
    expect(attrFamily('process.pid')).toBe('process');
    expect(attrFamily('telemetry.sdk.language')).toBe('telemetry');
    expect(attrFamily('http.status_code')).toBe('http');
    expect(attrFamily('db.statement')).toBe('db');
    expect(attrFamily('banking.account_id')).toBe('other');
  });
  it('separates resource families from span families', () => {
    expect(isResourceAttrKey('k8s.pod.name')).toBe(true);
    expect(isResourceAttrKey('service.version')).toBe(true);
    expect(isResourceAttrKey('http.method')).toBe(false);
    expect(isResourceAttrKey('db.system')).toBe(false);
  });
});

describe('span kind / status', () => {
  it('normalises wire forms', () => {
    expect(normSpanKind('SPAN_KIND_SERVER')).toBe('server');
    expect(normSpanKind('client')).toBe('client');
    expect(normSpanKind('5')).toBe('consumer');
    expect(normSpanKind(undefined)).toBe('unspecified');
    expect(spanStatus('STATUS_CODE_ERROR')).toBe('error');
    expect(spanStatus('ok')).toBe('ok');
    expect(spanStatus('')).toBe('unset');
  });
});

const mkSpan = (over: Partial<SpanRow>): SpanRow => ({
  traceId: 't', spanId: 's', parentSpanId: '', name: 'op', kind: 'server',
  serviceName: 'svc', hostName: 'h', startTime: 0, endTime: 0, durationMs: 1,
  statusCode: 'ok', statusMessage: '', attributes: {}, resourceAttributes: {},
  events: null, scopeName: '', ...over,
});

describe('span links + exceptions', () => {
  it('extracts indexed links and JSON-blob links', () => {
    expect(extractSpanLinks(mkSpan({ attributes: { 'otel.link.0.trace_id': 'abc', 'otel.link.0.span_id': 'def' } }))).toEqual([
      { traceId: 'abc', spanId: 'def', attributes: {} },
    ]);
    expect(extractSpanLinks(mkSpan({ attributes: { 'otel.links': '[{"trace_id":"x","span_id":"y"}]' } }))[0].traceId).toBe('x');
    expect(extractSpanLinks(mkSpan({}))).toEqual([]);
  });

  it('pulls exception events and flags error-by-exception', () => {
    const s = mkSpan({
      statusCode: 'unset',
      events: [{ name: 'exception', timeNano: 5, attributes: { 'exception.type': 'NPE', 'exception.message': 'boom' } }],
    });
    const ex = spanExceptions(s);
    expect(ex[0].type).toBe('NPE');
    expect(ex[0].message).toBe('boom');
    // status unset but an exception fired → still an error.
    expect(spanHasError(s)).toBe(true);
    expect(spanHasError(mkSpan({ statusCode: 'ok' }))).toBe(false);
  });
});
