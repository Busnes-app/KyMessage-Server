import { useEffect, useRef, useState } from 'react';
import { ShieldCheck } from 'lucide-react';
import { onReauthRequired, type Reauth } from '../api';

/** The console's one step-up prompt, mounted once and opened by adminFetch. Refusals that
 * arrive while it is open wait on the same answer; resolvers live in a ref so an answer
 * given before a queued refusal has rendered still settles it. */
export function ConfirmItsYou() {
  const [pending, setPending] = useState<Reauth | null>(null);
  const resolvers = useRef<((retry: boolean) => void)[]>([]);
  const dialog = useRef<HTMLDialogElement>(null);
  const settle = (retry: boolean) => resolvers.current.splice(0).forEach((resolve) => resolve(retry));
  useEffect(() => {
    const off = onReauthRequired((r) => new Promise<boolean>((resolve) => {
      resolvers.current.push(resolve);
      setPending((p) => p ?? r);
    }));
    return () => { off(); settle(false); };
  }, []);
  useEffect(() => {
    if (pending && !dialog.current?.open) dialog.current?.showModal();
  }, [pending]);
  const answer = (retry: boolean) => {
    settle(retry);
    dialog.current?.close();
    setPending(null);
  };
  if (!pending) return null;
  return (
    <dialog ref={dialog} className="modal-window" aria-labelledby="confirm-title"
      onClose={() => answer(false)}
      onCancel={(e) => { e.preventDefault(); answer(false); }}>
      <h3 id="confirm-title" style={{ display: 'flex', alignItems: 'center', gap: '8px', fontSize: '18px', marginBottom: '12px' }}>
        <ShieldCheck size={20} style={{ color: 'var(--accent)' }} />
        {"Confirm it's you"}
      </h3>
      <p style={{ color: 'var(--ink)', fontSize: '14px', marginBottom: '12px' }}>{pending.message}</p>
      {pending.url ? (
        <p style={{ fontSize: '14px', marginBottom: '16px' }}>
          <a href={pending.url} target="_blank" rel="noopener noreferrer">Sign in to KyIdentity again</a>
          {' '}in the new tab, then come back here and retry.
        </p>
      ) : (
        <p style={{ fontSize: '14px', marginBottom: '16px' }}>
          In another tab, sign out and sign in again, then come back here and retry.
        </p>
      )}
      <div className="dr-actions" style={{ justifyContent: 'flex-end' }}>
        <button type="button" className="btn-secondary" onClick={() => answer(false)}>Cancel</button>
        <button type="button" onClick={() => answer(true)}>Retry</button>
      </div>
    </dialog>
  );
}
