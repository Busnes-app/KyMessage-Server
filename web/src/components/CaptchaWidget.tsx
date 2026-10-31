import React, { useEffect, useRef, useState } from 'react';
import { ShieldCheck, Loader2, AlertCircle } from 'lucide-react';
import { count, obj, str } from '../dto';

interface CaptchaProps {
  onToken: (token: string) => void;
  /** Change it after each sign-in attempt: the server spends the token before checking the password. */
  round: number;
}

function parseChallenge(v: unknown) {
  const o = obj(v);
  return {
    algorithm: str(o.algorithm, 32), salt: str(o.salt, 128), challenge: str(o.challenge, 128),
    maxnumber: count(o.maxnumber), expires_at: count(o.expires_at), signature: str(o.signature, 256),
  };
}

// A new challenge shortly before the old one expires; never sooner than a minute, so a client
// clock far ahead of the server cannot turn expiry into a fetch loop.
const refreshDelay = (expiresAt: number) => Math.max(60_000, expiresAt * 1000 - Date.now() - 10_000);

/** Fetches and solves one challenge per mount, per `round`, and on expiry; never per render. */
export const CaptchaWidget: React.FC<CaptchaProps> = ({ onToken, round }) => {
  const [status, setStatus] = useState<'working' | 'done' | 'failed'>('working');
  const [progress, setProgress] = useState<number>(0);
  const [errorMsg, setErrorMsg] = useState<string>('');
  const [expired, setExpired] = useState<number>(0);
  const onTokenRef = useRef(onToken);
  useEffect(() => { onTokenRef.current = onToken; });

  useEffect(() => {
    let cancelled = false;
    let expiry: ReturnType<typeof setTimeout> | undefined;
    setStatus('working');
    setProgress(0);

    const runPoW = async () => {
      try {
        const resp = await fetch('/api/auth/pow-challenge');
        if (!resp.ok) throw new Error('Failed to fetch security challenge');
        const chal = parseChallenge(await resp.json());

        const enc = new TextEncoder();
        const max = chal.maxnumber;
        let solved = false;

        for (let i = 1; i <= max; i++) {
          if (cancelled) return;
          const target = `${chal.salt}${i}`;
          const hashBuf = await crypto.subtle.digest('SHA-256', enc.encode(target));
          const hashHex = Array.from(new Uint8Array(hashBuf))
            .map((b) => b.toString(16).padStart(2, '0'))
            .join('');

          if (hashHex === chal.challenge) {
            const sol = {
              algorithm: chal.algorithm,
              salt: chal.salt,
              challenge: chal.challenge,
              number: i,
              maxnumber: chal.maxnumber,
              expires_at: chal.expires_at,
              signature: chal.signature,
            };
            const token = btoa(JSON.stringify(sol));
            if (cancelled) return;
            setStatus('done');
            onTokenRef.current(token);
            expiry = setTimeout(() => {
              onTokenRef.current('');
              setExpired((n) => n + 1);
            }, refreshDelay(chal.expires_at));
            solved = true;
            break;
          }

          if (i % 2000 === 0) {
            setProgress(Math.round((i / max) * 100));
            await new Promise((r) => setTimeout(r, 0)); // yield loop
          }
        }

        if (!solved && !cancelled) {
          throw new Error('Could not solve challenge');
        }
      } catch (err) {
        if (!cancelled) {
          setStatus('failed');
          setErrorMsg(err instanceof Error ? err.message : 'Security check failed');
        }
      }
    };

    void runPoW();
    return () => {
      cancelled = true;
      clearTimeout(expiry);
    };
  }, [round, expired]);

  return (
    <p className="ky-auth-check" data-ky-state={status === 'done' ? 'passed' : status === 'failed' ? 'failed' : undefined} aria-live="polite">
      {status === 'working' && <><Loader2 className="animate-spin" aria-hidden="true" /><span>Checking this browser… {progress}%</span></>}
      {status === 'done' && <><ShieldCheck aria-hidden="true" /><span>Browser check passed</span></>}
      {status === 'failed' && <><AlertCircle aria-hidden="true" /><span>{errorMsg}</span></>}
    </p>
  );
};
