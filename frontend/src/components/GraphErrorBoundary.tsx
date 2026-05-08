'use client';
import { Component, ReactNode } from 'react';

// GraphErrorBoundary catches rendering failures in the heavy
// service-graph canvas (huge edge sets, NaN positions from layout
// edge cases, etc.) so they surface as an inline banner instead of
// blanking the whole /graph page. Reload restarts state, which
// often clears the issue (smaller window, narrower service filter).
interface State { error: Error | null }

export class GraphErrorBoundary extends Component<{ children: ReactNode }, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error) {
    // eslint-disable-next-line no-console
    console.error('[ServiceGraph] render failed:', error);
  }

  render() {
    if (this.state.error) {
      return (
        <div style={{
          padding: 24, margin: '12px 0', borderRadius: 8,
          background: 'rgba(255,82,82,.08)', border: '1px solid rgba(255,82,82,.30)',
        }}>
          <div style={{ fontSize: 14, fontWeight: 600, color: 'var(--err)', marginBottom: 8 }}>
            Service graph could not render
          </div>
          <div style={{ fontSize: 12, color: 'var(--text2)', lineHeight: 1.6, marginBottom: 12 }}>
            The topology engine hit an internal error rendering this view. This typically
            happens with very large or malformed graphs (thousands of services / edges).
            Try narrowing the time window, raising the &quot;Min calls&quot; slider, or filtering by
            a single service to drill into its neighborhood.
          </div>
          {this.state.error.message && (
            <pre style={{
              fontSize: 11, color: 'var(--text3)', background: 'var(--bg2)',
              padding: 10, borderRadius: 4, overflow: 'auto', maxHeight: 120,
              fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
            }}>{this.state.error.message}</pre>
          )}
          <button onClick={() => this.setState({ error: null })}
                  style={{ marginTop: 8, fontSize: 12, padding: '4px 12px' }}>
            Retry
          </button>
        </div>
      );
    }
    return this.props.children;
  }
}
