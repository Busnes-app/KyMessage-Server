import './theme';
import React from 'react';
import ReactDOM from 'react-dom/client';
import { App } from './App';
import { Meet } from './pages/Meet';
import { watchForUpdate } from './ky-ui/update.js';
import { ErrorBoundary } from './components/ErrorBoundary';

// Keep registration in the bundled script so script-src 'self' permits it.
if (import.meta.env.PROD && 'serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    void navigator.serviceWorker.register('/sw.js').catch(() => {});
  }, { once: true });
}

watchForUpdate({ name: 'KyMessages' });

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ErrorBoundary>
      {window.location.pathname === '/meet' ? <Meet /> : <App />}
    </ErrorBoundary>
  </React.StrictMode>
);
