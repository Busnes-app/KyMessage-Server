import React from 'react';

/** Shows a render failure instead of unmounting the tree into a blank page. */
export class ErrorBoundary extends React.Component<{ children: React.ReactNode }, { error: string | null }> {
  state = { error: null as string | null };

  static getDerivedStateFromError(error: unknown) {
    return { error: error instanceof Error && error.message ? error.message : 'Unknown error' };
  }

  render() {
    if (this.state.error === null) return this.props.children;
    return (
      <div className="dr-alert dr-alert-error" role="alert" style={{ margin: 16 }}>
        <span>
          This page failed to load: {this.state.error}.{' '}
          <button type="button" className="btn-secondary" onClick={() => window.location.reload()}>Reload</button>
        </span>
      </div>
    );
  }
}
