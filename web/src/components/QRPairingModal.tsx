import React, { useEffect, useLayoutEffect, useRef, useState } from 'react';
import QRCode from 'qrcode';
import { Smartphone, CheckCircle, X } from 'lucide-react';
import { errorMessage, secureFetch } from '../api';
import { obj, opt, str } from '../dto';

interface QRPairingModalProps {
  onClose: () => void;
}

export const QRPairingModal: React.FC<QRPairingModalProps> = ({ onClose }) => {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const dialogRef = useRef<HTMLDialogElement>(null);
  useLayoutEffect(() => {
    const dialog = dialogRef.current;
    dialog?.showModal();
    return () => dialog?.close();
  }, []);
  const [secondsLeft, setSecondsLeft] = useState<number>(90);
  const [paired, setPaired] = useState<boolean>(false);
  const [deviceName, setDeviceName] = useState<string>('');
  const [error, setError] = useState<string>('');

  // Everything stops on unmount, at expiry, on a non-OK answer and once paired. Timers start
  // only after init resolves, so the abort check right before them is what keeps a modal
  // closed mid-init from leaking them.
  useEffect(() => {
    const ctrl = new AbortController();
    const timers: ReturnType<typeof setInterval>[] = [];
    const stop = () => timers.forEach(clearInterval);
    const fail = (err: unknown, fallback: string) => {
      stop();
      if (!ctrl.signal.aborted) setError(err instanceof Error && err.message ? err.message : fallback);
    };

    const poll = async (secret: string) => {
      try {
        const resp = await secureFetch('/api/devices/pair/poll', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ secret }),
          signal: ctrl.signal,
        });
        if (!resp.ok) throw new Error(await errorMessage(resp, 'Could not check the pairing'));
        const p = obj(await resp.json());
        if (p.status === 'consumed' && !ctrl.signal.aborted) {
          stop();
          setPaired(true);
          setDeviceName(opt(p.device_name, str) || 'Mobile Device');
        }
      } catch (err) {
        fail(err, 'Could not check the pairing');
      }
    };

    const init = async () => {
      try {
        const resp = await secureFetch('/api/devices/pair/init', { method: 'POST', signal: ctrl.signal });
        if (!resp.ok) throw new Error(await errorMessage(resp, 'Could not start pairing'));
        const data = obj(await resp.json());
        const secret = str(data.secret, 256);
        const payload = str(data.qr_payload, 2048);
        if (ctrl.signal.aborted) return;
        if (canvasRef.current) {
          await QRCode.toCanvas(canvasRef.current, payload, {
            width: 200,
            margin: 1,
            color: { dark: '#4deeea', light: '#0d0f14' },
          });
        }
        if (ctrl.signal.aborted) return;

        const expiry = Date.now() + 90 * 1000;
        timers.push(setInterval(() => {
          const remaining = Math.max(0, Math.round((expiry - Date.now()) / 1000));
          setSecondsLeft(remaining);
          if (remaining <= 0) fail(null, 'This code has expired. Close and start again.');
        }, 1000));
        timers.push(setInterval(() => void poll(secret), 1500));
      } catch (err) {
        fail(err, 'Could not start pairing');
      }
    };

    void init();
    return () => {
      ctrl.abort();
      stop();
    };
  }, []);

  return (
    <dialog ref={dialogRef} className="modal-window" aria-labelledby="pairing-title" onCancel={onClose}
      onClick={(event) => {
        if (event.target !== event.currentTarget) return;
        const rect = event.currentTarget.getBoundingClientRect();
        if (event.clientX < rect.left || event.clientX > rect.right ||
            event.clientY < rect.top || event.clientY > rect.bottom) onClose();
      }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '16px' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
            <Smartphone size={20} style={{ color: 'var(--accent)' }} />
            <h3 id="pairing-title" style={{ fontSize: '18px' }}>Link Mobile Device</h3>
          </div>
          <button className="btn-secondary" style={{ padding: '4px' }} onClick={onClose} aria-label="Close modal">
            <X size={18} />
          </button>
        </div>

        {paired ? (
          <div style={{ textAlign: 'center', padding: '24px 0' }}>
            <CheckCircle size={48} style={{ color: 'var(--success)', margin: '0 auto 12px' }} />
            <h4 style={{ fontSize: '16px', color: 'var(--ink-strong)', marginBottom: '4px' }}>Device Linked!</h4>
            <p style={{ color: 'var(--ink)', fontSize: '14px' }}>{deviceName} is now securely paired with your account.</p>
            <button style={{ marginTop: '16px' }} onClick={onClose}>
              Done
            </button>
          </div>
        ) : error ? (
          <div className="dr-alert dr-alert-error" role="alert"><span>{error}</span></div>
        ) : (
          <div>
            <p style={{ color: 'var(--ink)', fontSize: '13px', marginBottom: '16px' }}>
              Scan this QR code with a compatible suite client.
            </p>

            <div
              style={{
                display: 'flex',
                justifyContent: 'center',
                background: '#0d0f14',
                padding: '16px',
                borderRadius: '8px',
                border: '1px solid var(--line)',
                marginBottom: '16px',
              }}
            >
              <canvas ref={canvasRef} />
            </div>

            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', fontSize: '12px', color: 'var(--ink)' }}>
              <span>Code expires in:</span>
              <span className="badge badge-accent" style={{ fontFamily: 'var(--font-mono)' }}>
                {secondsLeft}s
              </span>
            </div>
          </div>
        )}
    </dialog>
  );
};
