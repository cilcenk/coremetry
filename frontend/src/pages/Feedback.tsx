import { useState, useCallback } from 'react';
import { Spinner, Empty } from '@/components/Spinner';
import { Button } from '@/components/ui/Button';
import { api } from '@/lib/api';
import type { Feedback } from '@/lib/types';

const PAGE_SIZE = 20;

function formatDate(unixNs: number): string {
  return new Date(unixNs / 1_000_000).toLocaleString();
}

export default function FeedbackPage() {
  const [feedbacks, setFeedbacks] = useState<Feedback[] | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [message, setMessage] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);

  const load = useCallback(async (nextOffset: number, append: boolean) => {
    if (nextOffset === 0) setLoading(true);
    else setLoadingMore(true);
    setError(null);
    try {
      const res = await api.listFeedbacks(PAGE_SIZE, nextOffset);
      if (!res) {
        setError('Failed to load feedback.');
        return;
      }
      setFeedbacks(prev => append ? [...(prev ?? []), ...res.feedbacks] : res.feedbacks);
      setHasMore(res.hasMore);
      setOffset(nextOffset + res.feedbacks.length);
    } catch {
      setError('Failed to load feedback.');
    } finally {
      setLoading(false);
      setLoadingMore(false);
    }
  }, []);

  // Initial load
  useState(() => { load(0, false); });

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    const trimmed = message.trim();
    if (!trimmed) return;
    setSubmitting(true);
    setSubmitError(null);
    try {
      const saved = await api.submitFeedback(trimmed);
      if (!saved) {
        setSubmitError('Submission failed. Please try again.');
        return;
      }
      setMessage('');
      setFeedbacks(prev => [saved, ...(prev ?? [])]);
    } catch {
      setSubmitError('Submission failed. Please try again.');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div style={{ maxWidth: 720, margin: '0 auto', padding: '24px 16px' }}>
      <h1 style={{ fontSize: 20, fontWeight: 600, marginBottom: 8 }}>Feedback</h1>
      <p style={{ color: 'var(--fg-muted)', marginBottom: 24, fontSize: 14 }}>
        Share your thoughts about Coremetry. All feedback is visible to the team.
      </p>

      {/* Submission form */}
      <form onSubmit={handleSubmit} style={{ marginBottom: 32 }}>
        <textarea
          value={message}
          onChange={e => setMessage(e.target.value)}
          placeholder="Write your feedback here…"
          maxLength={2000}
          rows={4}
          disabled={submitting}
          style={{
            width: '100%',
            boxSizing: 'border-box',
            padding: '10px 12px',
            borderRadius: 6,
            border: '1px solid var(--border)',
            background: 'var(--bg-input, var(--bg))',
            color: 'var(--fg)',
            fontSize: 14,
            resize: 'vertical',
            fontFamily: 'inherit',
          }}
        />
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginTop: 8 }}>
          <Button
            variant="primary"
            type="submit"
            disabled={submitting || !message.trim()}
          >
            {submitting ? 'Submitting…' : 'Submit feedback'}
          </Button>
          <span style={{ fontSize: 12, color: 'var(--fg-muted)' }}>
            {message.length}/2000
          </span>
          {submitError && (
            <span style={{ fontSize: 13, color: 'var(--color-error, #e55)' }}>
              {submitError}
            </span>
          )}
        </div>
      </form>

      {/* List */}
      {loading && <Spinner />}
      {error && !loading && (
        <Empty icon="⚠" title={error} />
      )}
      {!loading && !error && feedbacks !== null && feedbacks.length === 0 && (
        <Empty icon="◇" title="No feedback yet. Be the first." />
      )}
      {feedbacks !== null && feedbacks.length > 0 && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          {feedbacks.map(f => (
            <div
              key={f.id}
              style={{
                padding: '14px 16px',
                borderRadius: 8,
                border: '1px solid var(--border)',
                background: 'var(--bg-card, var(--bg))',
              }}
            >
              <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 6 }}>
                <span style={{ fontSize: 13, fontWeight: 500 }}>{f.userEmail || 'Anonymous'}</span>
                <span style={{ fontSize: 12, color: 'var(--fg-muted)' }}>{formatDate(f.createdAt)}</span>
              </div>
              <p style={{ margin: 0, fontSize: 14, whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
                {f.message}
              </p>
            </div>
          ))}

          {hasMore && (
            <div style={{ textAlign: 'center', marginTop: 8 }}>
              <Button
                variant="secondary"
                onClick={() => load(offset, true)}
                disabled={loadingMore}
              >
                {loadingMore ? 'Loading…' : 'Load more'}
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
