import React, { useEffect, useState } from 'react';
import {
  AlertCircle,
  CheckCircle2,
  Clock,
  Download,
  HardDrive,
  KeyRound,
  Link2,
  Loader2,
  Play,
  RefreshCw,
  Send,
  Server,
  Unlink,
  XCircle,
} from 'lucide-react';
import { adminFetch, errorMessage, errorText } from '../api';
import { arr, bool, count, iso, obj, opt, str } from '../dto';
import { PageHead } from '../components/PageHead';
import { Status, type Tone } from '../components/Status';

export interface DepositReceipt {
  capsule_id: string;
  digest: string;
  size_bytes: number;
  deposited_at: string;
}

export interface LocalCopy {
  name: string;
  size_bytes: number;
  created_at: string;
}

interface BackupAttempt {
  outcome: 'success' | 'warning' | 'failure' | 'unknown';
  trigger: 'scheduled' | 'cli' | 'admin';
  recorded_at: string;
  capsule_id: string;
}

export function backupAttempt(value: unknown): BackupAttempt | undefined {
  if (value === undefined) return undefined;
  if (typeof value !== 'object' || value === null ||
    !('outcome' in value) || (value.outcome !== 'success' && value.outcome !== 'warning' && value.outcome !== 'failure' && value.outcome !== 'unknown') ||
    !('trigger' in value) || (value.trigger !== 'scheduled' && value.trigger !== 'cli' && value.trigger !== 'admin') ||
    !('recorded_at' in value) || typeof value.recorded_at !== 'string' || !Number.isFinite(Date.parse(value.recorded_at)) ||
    !('capsule_id' in value) || typeof value.capsule_id !== 'string') throw new Error('Invalid backup attempt status');
  return { outcome: value.outcome, trigger: value.trigger, recorded_at: value.recorded_at, capsule_id: value.capsule_id };
}

interface CapsuleSize {
  bytes: number;
  limit: number;
  percent: number;
  warning: boolean;
}

interface MediaRun {
  outcome: BackupAttempt['outcome'];
  trigger: BackupAttempt['trigger'];
  recorded_at: string;
  archive: string;
}

export function mediaRun(value: unknown): MediaRun | undefined {
  if (value === undefined) return undefined;
  if (typeof value !== 'object' || value === null || !('archive' in value) || typeof value.archive !== 'string') {
    throw new Error('Invalid media backup status');
  }
  try {
    const { outcome, trigger, recorded_at } = backupAttempt({ ...value, capsule_id: '' })!;
    return { outcome, trigger, recorded_at, archive: value.archive };
  } catch {
    throw new Error('Invalid media backup status');
  }
}

function mib(n: number): string {
  return `${(n / 1048576).toFixed(1)} MiB`;
}

export interface BackupStatus {
  paired: boolean;
  key_pinned: boolean;
  database_driver: string;
  members: string[];
  recovery_url?: string;
  recovery_key_id?: string;
  recovery_key_error?: string;
  threshold?: number;
  total_shares?: number;
  last_deposit?: DepositReceipt;
  last_run?: BackupAttempt;
  last_run_error?: string;
  capsule_size?: CapsuleSize;
  media_last_run?: MediaRun;
  media_last_run_error?: string;
  scheduler_error?: string;
  local_dir?: string;
  local_keep?: number;
  local_copies?: LocalCopy[];
  local_error?: string;
  interval_sec?: number;
  min_interval_sec?: number;
  next_run_at?: string;
}

interface DrillCheck {
  name: string;
  passed: boolean;
  message?: string;
}

interface DrillResult {
  passed: boolean;
  checks: DrillCheck[];
  error_message?: string;
  duration_ms: number;
}

interface RunResult {
  manifest: { capsule_id: string };
  size_bytes: number;
  local_path?: string;
  local_error?: string;
  receipt?: DepositReceipt;
  receipt_unrecorded?: boolean;
}

const HOUR = 3600;
const SCHEDULE_CHOICES: { label: string; sec: number }[] = [
  { label: 'Off', sec: 0 },
  { label: 'Every hour', sec: HOUR },
  { label: 'Every 6 hours', sec: 6 * HOUR },
  { label: 'Every 12 hours', sec: 12 * HOUR },
  { label: 'Daily', sec: 24 * HOUR },
  { label: 'Weekly', sec: 7 * 24 * HOUR },
];

function when(iso?: string): string {
  if (!iso) return '';
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

function every(sec?: number): string {
  if (!sec) return 'Off';
  const hit = SCHEDULE_CHOICES.find((c) => c.sec === sec);
  if (hit) return hit.label;
  return sec % HOUR === 0 ? `Every ${sec / HOUR} hours` : `Every ${Math.round(sec / 60)} minutes`;
}

function bytes(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MiB`;
  if (n >= 1 << 10) return `${Math.round(n / (1 << 10))} KiB`;
  return `${n} B`;
}

async function call(path: string, init: RequestInit, fallback: string): Promise<unknown> {
  const res = await adminFetch(path, init);
  if (!res.ok) throw new Error(await errorMessage(res, fallback));
  return res.json();
}

const optStr = (v: unknown) => opt(v, str);

function receipt(v: unknown): DepositReceipt {
  const o = obj(v);
  return { capsule_id: str(o.capsule_id, 128), digest: str(o.digest, 256), size_bytes: count(o.size_bytes), deposited_at: iso(o.deposited_at) };
}

function localCopy(v: unknown): LocalCopy {
  const o = obj(v);
  return { name: str(o.name, 255), size_bytes: count(o.size_bytes), created_at: iso(o.created_at) };
}

export function parseBackupStatus(v: unknown): BackupStatus {
  const o = obj(v);
  return {
    paired: bool(o.paired),
    key_pinned: bool(o.key_pinned),
    database_driver: str(o.database_driver, 32),
    members: (opt(o.members, arr) ?? []).map((m) => str(m)),
    recovery_url: optStr(o.recovery_url),
    recovery_key_id: opt(o.recovery_key_id, (k) => str(k, 128)),
    recovery_key_error: optStr(o.recovery_key_error),
    threshold: opt(o.threshold, count),
    total_shares: opt(o.total_shares, count),
    last_deposit: opt(o.last_deposit, receipt),
    last_run: backupAttempt(o.last_run),
    last_run_error: optStr(o.last_run_error),
    capsule_size: opt(o.capsule_size, (c) => {
      const r = obj(c);
      return { bytes: count(r.bytes), limit: count(r.limit), percent: count(r.percent), warning: bool(r.warning) };
    }),
    media_last_run: mediaRun(o.media_last_run),
    media_last_run_error: optStr(o.media_last_run_error),
    scheduler_error: optStr(o.scheduler_error),
    local_dir: opt(o.local_dir, (d) => str(d, 4096)),
    local_keep: opt(o.local_keep, count),
    local_copies: opt(o.local_copies, (c) => arr(c).map(localCopy)),
    local_error: optStr(o.local_error),
    interval_sec: opt(o.interval_sec, count),
    min_interval_sec: opt(o.min_interval_sec, count),
    next_run_at: opt(o.next_run_at, iso),
  };
}

function parseRunResult(v: unknown): RunResult {
  const o = obj(v);
  return {
    manifest: { capsule_id: str(obj(o.manifest).capsule_id, 128) },
    size_bytes: count(o.size_bytes),
    local_path: opt(o.local_path, (p) => str(p, 4096)),
    local_error: optStr(o.local_error),
    receipt: opt(o.receipt, receipt),
    receipt_unrecorded: opt(o.receipt_unrecorded, bool),
  };
}

function parseDrillResult(v: unknown): DrillResult {
  const o = obj(v);
  return {
    passed: bool(o.passed),
    checks: (opt(o.checks, arr) ?? []).map((c) => {
      const r = obj(c);
      return { name: str(r.name, 255), passed: bool(r.passed), message: optStr(r.message) };
    }),
    error_message: optStr(o.error_message),
    duration_ms: count(o.duration_ms),
  };
}

/** The key's ID as the server derives it: hex SHA-256 of the decoded public key. Null when
 * the text is not base64 or WebCrypto is unavailable (a page not served over https). */
export async function recoveryKeyID(publicKey: string): Promise<string | null> {
  let raw: Uint8Array;
  try {
    raw = Uint8Array.from(atob(publicKey.replace(/\s+/g, '')), (c) => c.charCodeAt(0));
  } catch {
    return null;
  }
  if (!globalThis.crypto?.subtle) return null;
  const sum = new Uint8Array(await crypto.subtle.digest('SHA-256', raw));
  return Array.from(sum, (b) => b.toString(16).padStart(2, '0')).join('');
}


const Alert: React.FC<{ kind: 'error' | 'warn' | 'success'; children: React.ReactNode }> = ({ kind, children }) => (
  <div className={`dr-alert dr-alert-${kind}`} role={kind === 'success' ? 'status' : 'alert'}>
    {kind === 'success' ? <CheckCircle2 size={16} /> : kind === 'error' ? <XCircle size={16} /> : <AlertCircle size={16} />}
    <span>{children}</span>
  </div>
);

export const Backup: React.FC = () => {
  const [status, setStatus] = useState<BackupStatus | null>(null);
  const [loadingStatus, setLoadingStatus] = useState<boolean>(true);
  const [statusError, setStatusError] = useState<string>('');

  const [running, setRunning] = useState<boolean>(false);
  const [runMessage, setRunMessage] = useState<string>('');
  const [runError, setRunError] = useState<string>('');

  const [runningDrill, setRunningDrill] = useState<boolean>(false);
  const [drillResult, setDrillResult] = useState<DrillResult | null>(null);

  const [scheduleSec, setScheduleSec] = useState<number>(24 * HOUR);
  const [scheduleSaving, setScheduleSaving] = useState<boolean>(false);
  const [scheduleMessage, setScheduleMessage] = useState<string>('');
  const [scheduleError, setScheduleError] = useState<string>('');

  const [remoteUrl, setRemoteUrl] = useState<string>('');
  const [pairCode, setPairCode] = useState<string>('');
  const [pairing, setPairing] = useState<boolean>(false);
  const [pairMessage, setPairMessage] = useState<string>('');
  const [pairError, setPairError] = useState<string>('');
  const [unpairing, setUnpairing] = useState<boolean>(false);

  const [pinKey, setPinKey] = useState<string>('');
  const [pinK, setPinK] = useState<string>('2');
  const [pinN, setPinN] = useState<string>('3');
  const [pinning, setPinning] = useState<boolean>(false);
  const [pinError, setPinError] = useState<string>('');

  const fetchStatus = async () => {
    setLoadingStatus(true);
    setStatusError('');
    try {
      const data = parseBackupStatus(await call('/api/backup/status', { method: 'GET' }, 'Could not load backup status'));
      setStatus(data);
      if (data.recovery_url) setRemoteUrl(data.recovery_url);
      if (typeof data.interval_sec === 'number') setScheduleSec(data.interval_sec);
    } catch (err) {
      setStatusError(errorText(err, 'Could not load backup status'));
    } finally {
      setLoadingStatus(false);
    }
  };

  useEffect(() => {
    void fetchStatus();
  }, []);

  const runBackup = async () => {
    setRunning(true);
    setRunMessage('');
    setRunError('');
    try {
      const res = parseRunResult(await call('/api/backup/deposit', { method: 'POST' }, 'Backup failed'));
      const went: string[] = [];
      if (res.local_path) went.push(`written to ${res.local_path}`);
      if (res.receipt) went.push(`deposited with KyRecovery at ${when(res.receipt.deposited_at)}`);
      setRunMessage(`Capsule ${res.manifest.capsule_id} (${bytes(res.size_bytes)}) ${went.join(' and ')}.`);
      if (res.local_error) setRunError(`The local copy failed: ${res.local_error}`);
      if (res.receipt_unrecorded) setRunError('KyRecovery holds the capsule but the receipt could not be recorded here; check the audit log.');
    } catch (err) {
      setRunError(errorText(err, 'Backup failed'));
    } finally {
      setRunning(false);
      await fetchStatus();
    }
  };

  /** Export is a CSRF-protected POST, so the file is fetched and saved from a blob. */
  const downloadCapsule = async () => {
    setRunError('');
    setRunMessage('');
    try {
      const res = await adminFetch('/api/backup/export-capsule', { method: 'POST' });
      if (!res.ok) throw new Error(await errorMessage(res, 'Download failed'));
      const match = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '');
      const url = URL.createObjectURL(await res.blob());
      const a = document.createElement('a');
      a.href = url;
      a.download = match ? match[1] : 'backup.kycap';
      a.click();
      URL.revokeObjectURL(url);
    } catch (err) {
      setRunError(errorText(err, 'Could not download the capsule'));
    }
  };

  const runDrill = async () => {
    setRunningDrill(true);
    setDrillResult(null);
    try {
      setDrillResult(parseDrillResult(await call('/api/backup/drill', { method: 'POST' }, 'Restore drill failed to run')));
    } catch (err) {
      const message = errorText(err, 'Restore drill failed to run');
      setDrillResult({ passed: false, duration_ms: 0, error_message: message, checks: [{ name: 'Execution', passed: false, message }] });
    } finally {
      setRunningDrill(false);
    }
  };

  const saveSchedule = async (e: React.FormEvent) => {
    e.preventDefault();
    setScheduleSaving(true);
    setScheduleMessage('');
    setScheduleError('');
    try {
      const saved = count(obj(await call(
        '/api/backup/schedule',
        { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ interval_sec: scheduleSec }) },
        'Could not save the schedule',
      )).interval_sec);
      setScheduleMessage(saved === 0 ? 'Automatic backups are off.' : `Backing up ${every(saved).toLowerCase()}.`);
      await fetchStatus();
    } catch (err) {
      setScheduleError(errorText(err, 'Could not save the schedule'));
    } finally {
      setScheduleSaving(false);
    }
  };

  const pair = async (e: React.FormEvent) => {
    e.preventDefault();
    const url = remoteUrl.trim();
    // A first pairing pins the key KyRecovery hands over, for good.
    if (
      !keyPinned &&
      !window.confirm(
        `Pair with ${url}?\n\nKyRecovery hands this server its suite recovery key, and the key is pinned for good: every capsule is sealed to it and this server never accepts another. Pair only with a KyRecovery you run or trust.`,
      )
    )
      return;
    setPairing(true);
    setPairMessage('');
    setPairError('');
    try {
      const res = obj(await call(
        '/api/backup/pair-remote',
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ recovery_url: url, pairing_code: pairCode.trim() }),
        },
        'Pairing failed',
      ));
      setPairMessage(
        `Paired with ${url}. Recovery key ${str(res.recovery_key_id, 128)} (${count(res.threshold)} of ${count(res.total_shares)} custodian cards) is pinned.`,
      );
      setPairCode('');
      await fetchStatus();
    } catch (err) {
      setPairError(errorText(err, 'Pairing failed'));
    } finally {
      setPairing(false);
    }
  };

  const unpair = async () => {
    if (
      !window.confirm(
        'Unpair from KyRecovery?\n\nRemoves the URL and sealed token rows. The key pin, receipts and local copies stay. The credential is dead only when the KyRecovery admin revokes it.',
      )
    )
      return;
    setUnpairing(true);
    setPairMessage('');
    setPairError('');
    try {
      await call('/api/backup/pairing', { method: 'DELETE' }, 'Could not unpair');
      setPairMessage('Unpaired. Off-site backups have stopped; ask the KyRecovery admin to revoke this service there.');
      await fetchStatus();
    } catch (err) {
      setPairError(errorText(err, 'Could not unpair'));
    } finally {
      setUnpairing(false);
    }
  };

  const pin = async (e: React.FormEvent) => {
    e.preventDefault();
    setPinError('');
    const id = await recoveryKeyID(pinKey);
    if (
      !window.confirm(
        `Pin this recovery key? This cannot be undone.\n\nKey ID: ${id ?? 'unavailable (not valid base64, or this page is not served over https)'}\nCustodian split: ${pinK} of ${pinN} cards\n\nEvery capsule from now on is sealed to this key, and this server never accepts another. Make sure it is the key from your ceremony.`,
      )
    )
      return;
    setPinning(true);
    try {
      await call(
        '/api/backup/pin-key',
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ public_key: pinKey.trim(), threshold: Number(pinK), total_shares: Number(pinN) }),
        },
        'Could not pin the key',
      );
      setPinKey('');
      await fetchStatus();
    } catch (err) {
      setPinError(errorText(err, 'Could not pin the key'));
    } finally {
      setPinning(false);
    }
  };

  const keyPinned = status?.key_pinned ?? false;
  const paired = status?.paired ?? false;
  const hasLocal = Boolean(status?.local_dir);
  const canBackUp = keyPinned && (paired || hasLocal);
  const scheduleOn = (status?.interval_sec ?? 0) > 0;
  const copies = status?.local_copies ?? [];
  const newestLocal = copies[0];
  // Green only for copies that exist: a set directory with none written, or a list the server did not send, is not a success.
  const localBadge: [tone: Tone | undefined, text: string] =
    !hasLocal ? [undefined, 'Off']
      : status?.local_error ? ['danger', 'Error']
      : !status?.local_copies ? [undefined, 'Unknown']
      : [copies.length ? 'success' : undefined, `${copies.length} of ${status.local_keep}`];

  return (
    <div>
      <PageHead title="Backup & recovery"
        action={<button type="button" className="btn-secondary" onClick={fetchStatus} disabled={loadingStatus}>
          <RefreshCw size={14} className={loadingStatus ? 'animate-spin' : ''} aria-hidden="true" />
          <span>Refresh</span>
        </button>} />

      {statusError && <Alert kind="error">{statusError}</Alert>}
      {status?.scheduler_error && <Alert kind="error">Automatic backups are not running: {status.scheduler_error}</Alert>}
      {status?.last_run_error && <Alert kind="error">{status.last_run_error}</Alert>}
      {status?.recovery_key_error && <Alert kind="error">{status.recovery_key_error}</Alert>}
      {status && status.database_driver !== 'sqlite' && (
        <Alert kind="warn">
          Backups do not include the PostgreSQL database: a capsule can only snapshot SQLite. Back this database up
          with pg_dump on its own schedule.
        </Alert>
      )}
      {status && !keyPinned && <Alert kind="warn">No backups are being made. Pair with KyRecovery or pin the suite recovery key below.</Alert>}
      {status && keyPinned && !paired && !hasLocal && (
        <Alert kind="warn">A key is pinned but capsules have nowhere to go. Pair with KyRecovery, or set KY_BACKUP_DIR to keep copies on this host.</Alert>
      )}
      {status && keyPinned && !scheduleOn && <Alert kind="warn">Automatic backups are off. Only the button below makes one.</Alert>}

      <div className="dr-facts">
        <div className="dr-fact">
          <div className="dr-fact-label">
            <span>Recovery key</span>
            <Status tone={keyPinned ? 'success' : 'danger'}>{keyPinned ? 'Pinned' : 'None'}</Status>
          </div>
          <div className="dr-fact-value dr-mono">{status?.recovery_key_id ?? '—'}</div>
          <div className="dr-fact-note">
            {keyPinned ? `${status?.threshold} of ${status?.total_shares} custodian cards open a capsule` : 'Nothing can be sealed until a key is pinned'}
          </div>
        </div>
        <div className="dr-fact">
          <div className="dr-fact-label">
            <span>KyRecovery</span>
            <Status tone={paired ? 'success' : undefined}>{paired ? 'Paired' : 'Not paired'}</Status>
          </div>
          <div className="dr-fact-value">{paired ? status?.recovery_url : 'No off-site copy'}</div>
          <div className="dr-fact-note">
            {status?.last_deposit ? `Last deposit ${when(status.last_deposit.deposited_at)}` : paired ? 'Nothing deposited yet' : ''}
          </div>
        </div>
        <div className="dr-fact">
          <div className="dr-fact-label">
            <span>Local copies</span>
            <Status tone={localBadge[0]}>{localBadge[1]}</Status>
          </div>
          <div className="dr-fact-value dr-mono">{status?.local_dir ?? 'KY_BACKUP_DIR not set'}</div>
          <div className="dr-fact-note">{status?.local_error ?? (newestLocal ? `Newest ${when(newestLocal.created_at)}` : hasLocal ? 'Nothing written yet' : '')}</div>
        </div>
        <div className="dr-fact">
          <div className="dr-fact-label">
            <span>Schedule</span>
            <Status tone={!scheduleOn ? 'danger' : canBackUp ? 'success' : undefined}>{every(status?.interval_sec)}</Status>
          </div>
          <div className="dr-fact-value">
            {!scheduleOn ? 'Manual only' : !keyPinned ? 'Waiting for recovery key' : !canBackUp ? 'Waiting for a destination'
              : status?.next_run_at ? `Next ${when(status.next_run_at)}` : 'Next run not scheduled'}
          </div>
          <div className="dr-fact-note">Counts from the last attempt, successful or not</div>
        </div>
      </div>

      {status?.last_run ? (
        <Alert kind={status.last_run.outcome === 'success' ? 'success' : status.last_run.outcome === 'failure' ? 'error' : 'warn'}>
          Last recorded backup attempt: {{ success: 'Succeeded', warning: 'Needs attention', failure: 'Failed', unknown: 'Outcome unavailable' }[status.last_run.outcome]}
          {' — '}{status.last_run.trigger}, {when(status.last_run.recorded_at)}.
          {status.last_run.outcome !== 'success' && ' Check the service logs for details; existing copies are listed above.'}
          {status.last_run.capsule_id && <> Capsule: <code>{status.last_run.capsule_id}</code>.</>}
        </Alert>
      ) : status && !status.last_run_error && <p className="dr-hint">No recorded backup attempts.</p>}

      {status?.capsule_size && (
        <Alert kind={status.capsule_size.warning ? 'warn' : 'success'}>
          Last capsule: {mib(status.capsule_size.bytes)} of {mib(status.capsule_size.limit)} ({status.capsule_size.percent}%).
          {status.capsule_size.warning && ' Warning: at 75% or more of the limit; a larger capsule will fail to seal.'}
        </Alert>
      )}
      {status?.media_last_run_error && <Alert kind="error">{status.media_last_run_error}</Alert>}
      {status?.media_last_run && (
        <Alert kind={status.media_last_run.outcome === 'success' ? 'success' : status.media_last_run.outcome === 'failure' ? 'error' : 'warn'}>
          Last media backup: {{ success: 'Succeeded', warning: 'Needs attention', failure: 'Failed', unknown: 'Outcome unavailable' }[status.media_last_run.outcome] ?? 'Outcome unavailable'}
          {' — '}{status.media_last_run.trigger}, {when(status.media_last_run.recorded_at)}.
          {status.media_last_run.archive && <> Archive: <code>{status.media_last_run.archive}</code>.</>}
          {status.media_last_run.outcome !== 'success' && ' Check the service logs for details.'}
        </Alert>
      )}

      <div className="panel dr-section">
        <div className="panel-header">
          <h3>Back up now</h3>
          <div className="dr-actions">
            <button type="button" onClick={runBackup} disabled={running || !canBackUp}>
              {running ? <Loader2 size={14} className="animate-spin" /> : <Send size={14} />}
              <span>{running ? 'Sealing…' : 'Back up now'}</span>
            </button>
            <button type="button" className="btn-secondary" onClick={downloadCapsule} disabled={!keyPinned}>
              <Download size={14} />
              <span>Download capsule</span>
            </button>
            <button type="button" className="btn-secondary" onClick={runDrill} disabled={runningDrill}>
              {runningDrill ? <Loader2 size={14} className="animate-spin" /> : <Play size={14} />}
              <span>{runningDrill ? 'Restoring…' : 'Run restore drill'}</span>
            </button>
          </div>
        </div>
        <p className="dr-hint">
          One sealed capsule goes to every destination above: the local directory, and KyRecovery when paired. Download saves the same
          capsule to this browser instead. Nothing on this server can open a capsule; that takes{' '}
          {status?.threshold ? `${status.threshold} custodian cards` : 'the custodian cards'} together.
        </p>
        <div className="dr-hint">A capsule carries</div>
        <ul className="dr-members">
          {(status?.members ?? []).map((m) => (
            <li key={m}>{m}</li>
          ))}
        </ul>
        {runMessage && <Alert kind="success">{runMessage}</Alert>}
        {runError && <Alert kind="error">{runError}</Alert>}
        {copies.length > 0 && (
          <ul className="dr-copies">
            {copies.map((c) => (
              <li key={c.name}>
                <span>{c.name}</span>
                <span>
                  {bytes(c.size_bytes)} · {when(c.created_at)}
                </span>
              </li>
            ))}
          </ul>
        )}
        {drillResult && (
          <div className="dr-drill">
            <div>
              Restore drill <Status tone={drillResult.passed ? 'success' : 'danger'}>{drillResult.passed ? 'passed' : 'failed'}</Status>{' '}
              <span className="dr-hint">{drillResult.duration_ms} ms</span>
            </div>
            {drillResult.error_message && <div className="dr-danger">{drillResult.error_message}</div>}
            <div className="dr-checks">
              {drillResult.checks.map((check, idx) => (
                <div key={idx} className="dr-check">
                  {check.passed ? <CheckCircle2 size={14} className="dr-ok" /> : <XCircle size={14} className="dr-danger" />}
                  <strong>{check.name}</strong>
                  <span className="dr-hint">{check.message}</span>
                </div>
              ))}
            </div>
          </div>
        )}
      </div>

      <div className="panel dr-section">
        <div className="panel-header">
          <h3>Schedule</h3>
        </div>
        <form onSubmit={saveSchedule} className="dr-row">
          <label className="dr-field">
            <span>Back up automatically</span>
            <select value={scheduleSec} onChange={(e) => setScheduleSec(Number(e.target.value))}>
              {SCHEDULE_CHOICES.map((c) => (
                <option key={c.sec} value={c.sec}>
                  {c.label}
                </option>
              ))}
            </select>
          </label>
          <button type="submit" className="btn-secondary" disabled={scheduleSaving || scheduleSec === (status?.interval_sec ?? -1)}>
            {scheduleSaving ? <Loader2 size={14} className="animate-spin" /> : <Clock size={14} />}
            <span>Save</span>
          </button>
        </form>
        <p className="dr-hint">
          Each run snapshots the whole database, so the floor is {Math.round((status?.min_interval_sec ?? 900) / 60)} minutes. The schedule does
          nothing until a key is pinned and there is somewhere to send the capsule. KY_BACKUP_DEPOSIT_INTERVAL is only the default.
        </p>
        {scheduleMessage && <Alert kind="success">{scheduleMessage}</Alert>}
        {scheduleError && <Alert kind="error">{scheduleError}</Alert>}
      </div>

      <div className="dr-two">
        <div className="panel dr-section">
          <div className="panel-header">
            <h3>
              <Server size={16} /> KyRecovery
            </h3>
            <Status tone={paired ? 'success' : undefined}>{paired ? 'Paired' : 'Not paired'}</Status>
          </div>
          <p className="dr-hint">
            KyRecovery keeps capsules it cannot open, off this host. In its dashboard, generate a pairing code for this service and enter it
            here. Pairing hands this server the suite recovery key and a deposit credential;
            {paired ? ' re-pairing is only accepted with the same key.' : ' the key is pinned once and never replaced.'}
          </p>
          <form onSubmit={pair} className="dr-stack">
            <label className="dr-field">
              <span>Server URL</span>
              <input type="url" placeholder="https://recovery.example.com" value={remoteUrl} onChange={(e) => setRemoteUrl(e.target.value)} required />
            </label>
            <div className="dr-row">
              <label className="dr-field">
                <span>Pairing code</span>
                <input
                  className="dr-mono"
                  type="text"
                  inputMode="numeric"
                  placeholder="123456"
                  value={pairCode}
                  onChange={(e) => setPairCode(e.target.value)}
                  required
                />
              </label>
              <button type="submit" className="btn-secondary" disabled={pairing}>
                {pairing ? <Loader2 size={14} className="animate-spin" /> : <Link2 size={14} />}
                <span>{paired ? 'Re-pair' : 'Pair'}</span>
              </button>
            </div>
          </form>
          {paired && (
            <button type="button" className="btn-secondary" onClick={unpair} disabled={unpairing}>
              {unpairing ? <Loader2 size={14} className="animate-spin" /> : <Unlink size={14} />}
              <span>Unpair</span>
            </button>
          )}
          {pairMessage && <Alert kind="success">{pairMessage}</Alert>}
          {pairError && <Alert kind="error">{pairError}</Alert>}
        </div>

        <div className="panel dr-section">
          <div className="panel-header">
            <h3>
              <KeyRound size={16} /> Recovery key by hand
            </h3>
            <Status tone={keyPinned ? 'success' : 'danger'}>{keyPinned ? 'Pinned' : 'None'}</Status>
          </div>
          {keyPinned ? (
            <p className="dr-hint">
              The key is pinned{paired ? ' by pairing' : ''}. Rotating it means a new ceremony and a fresh data directory; there is no button
              for that on purpose.
            </p>
          ) : (
            <>
              <p className="dr-hint">
                For a server with no KyRecovery. Run the suite ceremony once, keep the custodian cards, and paste the public key it shows, with
                the split it used. Capsules then go to the local directory.
              </p>
              <form onSubmit={pin} className="dr-stack">
                <label className="dr-field">
                  <span>Suite recovery public key</span>
                  <textarea className="dr-mono" rows={4} value={pinKey} onChange={(e) => setPinKey(e.target.value)} placeholder="base64 from the ceremony page" required />
                </label>
                <div className="dr-row">
                  <label className="dr-field dr-narrow">
                    <span>Needed</span>
                    <input type="number" min={2} max={255} value={pinK} onChange={(e) => setPinK(e.target.value)} required />
                  </label>
                  <label className="dr-field dr-narrow">
                    <span>Of</span>
                    <input type="number" min={2} max={255} value={pinN} onChange={(e) => setPinN(e.target.value)} required />
                  </label>
                  <button type="submit" className="btn-secondary" disabled={pinning}>
                    {pinning ? <Loader2 size={14} className="animate-spin" /> : <KeyRound size={14} />}
                    <span>Pin key</span>
                  </button>
                </div>
              </form>
              {pinError && <Alert kind="error">{pinError}</Alert>}
            </>
          )}
          {hasLocal && (
            <p className="dr-hint">
              <HardDrive size={12} /> Local copies land in {status?.local_dir}; the newest {status?.local_keep} are kept.
            </p>
          )}
        </div>
      </div>
    </div>
  );
};
