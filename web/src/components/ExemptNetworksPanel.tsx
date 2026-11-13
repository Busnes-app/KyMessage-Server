import React, { useEffect, useState } from 'react';
import { Network } from 'lucide-react';
import { adminFetch, errorMessage, errorText } from '../api';
import { count, obj, str } from '../dto';

export interface ExemptStatus { networks: string; environment: string; exempted: number }

export const EXEMPT_ROUTE = '/api/settings/rate-limit-networks';

export function parseExemptStatus(v: unknown): ExemptStatus {
  const b = obj(v);
  return { networks: str(b.networks, 16384), environment: str(b.environment, 16384), exempted: count(b.exempted) };
}

/** Office networks whose sign-ins skip the per-address rate limit. One network per line. */
export const ExemptNetworksPanel: React.FC = () => {
  const [status, setStatus] = useState<ExemptStatus | null>(null);
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');

  const show = (s: ExemptStatus) => {
    setStatus(s);
    setText(s.networks.split(',').filter(Boolean).join('\n'));
  };

  useEffect(() => {
    const controller = new AbortController();
    fetch(EXEMPT_ROUTE, { signal: controller.signal, cache: 'no-store' })
      .then(async (res) => {
        if (!res.ok) throw new Error(await errorMessage(res, 'Could not load the networks'));
        show(parseExemptStatus(await res.json()));
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(errorText(err, 'Could not load the networks'));
      });
    return () => controller.abort();
  }, []);

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setMessage('');
    setError('');
    try {
      const res = await adminFetch(EXEMPT_ROUTE, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ networks: text.split(/[\s,]+/).filter(Boolean).join(',') }),
      });
      if (!res.ok) throw new Error(await errorMessage(res, 'Could not save the networks'));
      const saved = parseExemptStatus(await res.json());
      show(saved);
      setMessage(saved.networks ? 'Saved. These networks now skip the per-address limit.' : 'Saved. No network skips the limit.');
    } catch (err) {
      setError(errorText(err, 'Could not save the networks'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="panel dr-section" aria-labelledby="exempt-heading">
      <div className="panel-header">
        <h3 id="exempt-heading"><Network size={16} aria-hidden="true" /> Office networks</h3>
      </div>
      <p className="dr-hint">
        Sign-in is limited per network address. Staff in one office usually share one address and can trip that limit together. Networks
        listed here skip it; per-account limits and the security check still apply. Adding a network needs a sign-in from the last 10 minutes.
      </p>
      <form onSubmit={save}>
        <label className="dr-field">
          <span>Networks that skip the per-address limit, one per line</span>
          <textarea
            className="font-mono"
            rows={4}
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder="203.0.113.0/24"
            aria-describedby="exempt-hint"
            disabled={!status}
          />
        </label>
        <p id="exempt-hint" className="dr-hint">
          List only networks you control: everyone behind a listed address loses the limit, so never add guest Wi-Fi, a VPN provider or
          your reverse proxy. The widest range accepted is /16 (IPv4) or /48 (IPv6).
        </p>
        <div className="dr-actions"><button type="submit" disabled={busy || !status}>Save networks</button></div>
      </form>
      {message && <div className="dr-alert dr-alert-success" role="status">{message}</div>}
      {error && <div className="dr-alert dr-alert-error" role="alert">{error}</div>}
      {status && (
        <div className="dr-facts">
          {status.environment && (
            <div className="dr-fact">
              <div className="dr-fact-label">Also set by the server environment</div>
              <div className="dr-fact-value font-mono">{status.environment.split(',').join(', ')}</div>
            </div>
          )}
          <div className="dr-fact">
            <div className="dr-fact-label">Requests that skipped the limit</div>
            <div className="dr-fact-value">{status.exempted} since the last change or restart</div>
          </div>
        </div>
      )}
    </section>
  );
};
