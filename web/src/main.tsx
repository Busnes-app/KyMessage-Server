import './theme';
import React from 'react';
import ReactDOM from 'react-dom/client';
import { App } from './App';

// Keep registration in the bundled script so script-src 'self' permits it.
if (import.meta.env.PROD && 'serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    void navigator.serviceWorker.register('/sw.js').catch(() => {});
  }, { once: true });
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
