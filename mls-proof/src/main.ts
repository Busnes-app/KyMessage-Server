import { proof } from './device';
import { delivery } from './delivery';

// The existing SSO callback returns to /. Keep the return hint local and fixed;
// it carries no tokens and cannot redirect outside this isolated prototype.
if (sessionStorage.getItem('kymessages-oidc-return') === '1') {
  sessionStorage.removeItem('kymessages-oidc-return');
  location.replace('/chat.html?auth=oidc');
}

window.delivery = delivery;

window.proof = proof; // Deliberate test-driver surface, available only in this isolated build.

function field(id: string) {
  const element = document.getElementById(id);
  if (!(element instanceof HTMLInputElement || element instanceof HTMLTextAreaElement)) throw new Error(`Missing field: ${id}`);
  return element;
}

function buttons(id: string, actions: Record<string, () => unknown>) {
  const container = document.getElementById(id);
  if (!container) throw new Error(`Missing action container: ${id}`);
  for (const [label, action] of Object.entries(actions)) {
    const button = document.createElement('button');
    button.type = 'button';
    button.textContent = label;
    button.addEventListener('click', async () => {
      const status = document.getElementById('status');
      if (!status) throw new Error('Missing status');
      const controls = document.querySelectorAll('button');
      controls.forEach(control => { control.disabled = true; });
      try {
        const result = await action();
        field('result').value = typeof result === 'string' ? result : JSON.stringify(result ?? 'Done', null, 2);
        status.textContent = `${label}: complete`;
      } catch (error) {
        status.textContent = `${label}: ${error instanceof Error ? error.message : 'Failed'}`;
      } finally {
        controls.forEach(control => { control.disabled = false; });
      }
    });
    container.append(button);
  }
}

buttons('device-actions', {
  Initialize: async () => {
    try { return await proof.initialize(field('identity').value, field('password').value); }
    finally { field('password').value = ''; }
  },
  Unlock: async () => {
    try { return await proof.unlock(field('password').value); }
    finally { field('password').value = ''; }
  },
  Lock: () => { proof.lock(); field('result').value = ''; field('message').value = ''; },
  Status: () => proof.status(),
});
buttons('room-actions', {
  'Inspect fingerprint': () => proof.inspectKeyPackage(field('wire').value.trim()),
  'Approve device': () => proof.approve(field('wire').value.trim()),
  'Create room': () => proof.create(),
  'Stage add': () => proof.add(field('wire').value.trim()),
  'Accept staged commit': () => proof.settle(true),
  'Discard staged commit': () => proof.settle(false),
  'Join Welcome': () => proof.join(field('wire').value.trim()),
  Receive: () => proof.receive(Number(field('sequence').value), field('wire').value.trim()),
  'Stage removal': () => proof.remove(field('remove').value),
  'Stage key update': () => proof.update(),
});
buttons('message-actions', {
  Send: () => proof.send(field('message').value),
  'Acknowledge wire delivery': () => proof.acknowledge(field('wire').value.trim()),
});
