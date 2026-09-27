import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawn } from 'node:child_process';

// Always own disposable data. Never reuse a running development/production server.
const dir = await mkdtemp(join(tmpdir(), 'ky-browser-'));
const server = spawn(fileURLToPath(new URL('../../.browser/server', import.meta.url)), [], {
  cwd: dir,
  env: {
    PATH: process.env.PATH,
    KY_APP_URL: 'http://127.0.0.1:5391',
    KY_HOST: '127.0.0.1', KY_PORT: '5391', KY_DB_DRIVER: 'sqlite',
    KY_DATA_DIR: join(dir, 'data'), KY_BACKUP_DIR: join(dir, 'backups'),
    KY_ADMIN_PASSWORD: 'BrowserInitial123!', KY_CAPTCHA_PROVIDER: 'none',
  },
  stdio: 'inherit',
});
for (const signal of ['SIGTERM', 'SIGINT']) process.on(signal, () => server.kill('SIGTERM'));
server.on('error', async error => { console.error(error.message); await rm(dir, { recursive: true, force: true }); process.exit(1); });
server.on('exit', async code => { await rm(dir, { recursive: true, force: true }); process.exit(code ?? 1); });
