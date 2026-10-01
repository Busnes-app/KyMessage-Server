import { useEffect, useState } from 'react';

type Check = {peer_ip: string; client_ip: string; forwarded_trusted: boolean; forwarded_proto: string; app_url_https: boolean; host_matches: boolean};
function parse(v: unknown): Check {
  const c = (v ?? {}) as Record<string, unknown>;
  const s = (x: unknown) => { if (typeof x !== 'string' || x.length > 64) throw new Error('bad'); return x; };
  const b = (x: unknown) => { if (typeof x !== 'boolean') throw new Error('bad'); return x; };
  return {peer_ip: s(c.peer_ip), client_ip: s(c.client_ip), forwarded_trusted: b(c.forwarded_trusted), forwarded_proto: s(c.forwarded_proto), app_url_https: b(c.app_url_https), host_matches: b(c.host_matches)};
}
const mark = (pass: boolean, good: string, bad: string) => <li>{pass ? `Pass: ${good}` : `Warn: ${bad}`}</li>;

// Confirms how this request reached the server; see docs/Reverse_Proxy_Networking.md.
export function NetworkCheck() {
  const [check, setCheck] = useState<Check | null | 'error'>(null);
  useEffect(() => {
    const controller = new AbortController();
    fetch('/api/admin/network-check', {signal: controller.signal, cache: 'no-store'})
      .then(async r => { if (!r.ok) throw new Error('unavailable'); setCheck(parse(await r.json())); })
      .catch(() => { if (!controller.signal.aborted) setCheck('error'); });
    return () => controller.abort();
  }, []);
  if (check === null) return <p role="status">Checking the network path…</p>;
  if (check === 'error') return <p role="alert">Network check unavailable.</p>;
  return <section aria-labelledby="network-check"><h3 id="network-check">Network path</h3>
    <p>Your address as seen by the server: {check.client_ip} (direct peer {check.peer_ip}).</p>
    <ul>
      {mark(check.forwarded_trusted, 'the request came through a trusted proxy', 'the direct peer is not in KY_TRUSTED_PROXIES; forwarded headers are ignored')}
      {mark(check.app_url_https, 'KY_APP_URL is https', 'KY_APP_URL is not https')}
      {mark(check.host_matches, 'the request host matches KY_APP_URL', 'the request host does not match KY_APP_URL')}
    </ul></section>;
}
