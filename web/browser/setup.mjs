import { request, expect } from '@playwright/test';

export default async function setup(config) {
  const api = await request.newContext({ baseURL: config.projects[0].use.baseURL });
  try {
    await api.get('/');
    const login = await api.post('/api/auth/login', { data: { username: 'admin', password: 'BrowserInitial123!' } });
    expect(login.ok()).toBe(true);
    const state = await login.json();
    const cookies = (await api.storageState()).cookies;
    const csrf = cookies.find(cookie => cookie.name === 'ky_csrf')?.value;
    expect(csrf).toBeTruthy();
    expect(state.must_change_password ?? state.user?.must_change_password).toBe(true);
    const changed = await api.post('/api/auth/change-password', {
      headers: { 'X-CSRF-Token': csrf },
      data: { current_password: 'BrowserInitial123!', new_password: 'BrowserUpdated456!' },
    });
    expect(changed.ok(), JSON.stringify(state)).toBe(true);
    expect(changed.headers()['content-type']).toContain('application/json');
  } finally { await api.dispose(); }
}
