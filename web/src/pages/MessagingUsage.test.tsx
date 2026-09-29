import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MessagingUsage } from './MessagingUsage';
import { Dashboard } from './Dashboard';

const empty = {
  room_count:0, rooms:[], sampled_at:'2026-09-27T12:00:00Z',
  totals:{active_events:0,retained_bytes:0,receipts:0},
  limits:{active_events:100000,retained_bytes:536870912,receipts:1000000},
};
function respond(value: unknown, status = 200) {
  return new Response(JSON.stringify(value),{status,headers:{'Content-Type':'application/json'}});
}
function mockUsage(value: unknown) {
  vi.stubGlobal('fetch',vi.fn(async (input: RequestInfo | URL) => {
    expect(String(input)).toBe('/api/admin/messaging/usage');
    return respond(value);
  }));
}
afterEach(() => {cleanup();vi.unstubAllGlobals();});

it('shows retired room pressure without treating expired events as freed receipts',async () => {
  const totals = {active_events:0,retained_bytes:0,receipts:1000000};
  mockUsage({...empty,room_count:1,totals,rooms:[{id:'room',name:'<img src=x onerror=alert(1)>',retired:true,...totals}]});
  const {container} = render(<MessagingUsage />);
  expect(await screen.findByText(/At a room limit/)).toBeTruthy();
  expect(screen.getByText(/Retired/)).toBeTruthy();
  expect(screen.getByText(/needs a new conversation/)).toBeTruthy();
  expect(container.querySelector('img')).toBeNull();
  expect(screen.queryByText('No messaging rooms.')).toBeNull();
});

it('reports fetch failure instead of zero usage, then supports an explicit refresh',async () => {
  vi.stubGlobal('fetch',vi.fn().mockResolvedValueOnce(respond({},500)).mockResolvedValueOnce(respond(empty)));
  render(<MessagingUsage />);
  expect((await screen.findByRole('alert')).textContent).toContain('unavailable');
  expect(screen.queryByText('No messaging rooms.')).toBeNull();
  fireEvent.click(screen.getByRole('button',{name:'Refresh storage usage'}));
  expect(await screen.findByText('No messaging rooms.')).toBeTruthy();
  expect(screen.queryByRole('alert')).toBeNull();
});

it.each([
  {...empty,totals:{...empty.totals,active_events:-1}},
  {...empty,limits:{...empty.limits,receipts:0}},
  {...empty,sampled_at:'not a timestamp'},
])('rejects malformed usage at the response boundary',async value => {
  mockUsage(value);
  render(<MessagingUsage />);
  expect((await screen.findByRole('alert')).textContent).toContain('Invalid messaging');
  expect(screen.queryByText('No messaging rooms.')).toBeNull();
});

it('does not request operator metadata for a member dashboard',() => {
  const fetch = vi.fn();
  vi.stubGlobal('fetch',fetch);
  render(<Dashboard settings={null} user={{role:'user',username:'Member'}} onNavigate={() => {}} />);
  expect(screen.queryByRole('region',{name:'Messaging storage'})).toBeNull();
  expect(fetch).not.toHaveBeenCalled();
});
