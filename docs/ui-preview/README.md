# Ky server UI preview

These screenshots show the shared layout language proposed for the Ky servers.
The selected page uses a quiet surface plus a slim Busnes accent rail, matching
KyIdentity and KyDNS while keeping the navigation readable in both modes.

| Template | Representative applications | Preview |
| --- | --- | --- |
| Workspace shell | KyPost, KyVault, KyDNS | [01-workspace-shell.png](01-workspace-shell.png) |
| Resource browser | KyVault, KyMark, KyNotes | [02-resource-browser.png](02-resource-browser.png) |
| Operations console | KyIdentity, KyForge, KyRecovery | [03-operations-console.png](03-operations-console.png) |
| Status dashboard | KyDNS, KyNotes, KyYard | [04-status-dashboard.png](04-status-dashboard.png) |
| Settings canvas | KyRecovery, KyPost, KyDNS | [05-settings-canvas.png](05-settings-canvas.png) |

Open [index.html](index.html) locally to switch between all five examples and
preview Busnes Light or Busnes Dark.

## Theme review

- Busnes Light and Busnes Dark are the defaults and are working well.
- KyIdentity and KyDNS already had the desired selected-page accent rail; their
  treatment is the reference behavior.
- KyNotes previously kept hard-coded cream surfaces after selecting another
  palette. Its workspace now consumes the shared theme tokens.
- The older Cyber preset made headings neon and reduced hierarchy. Its accent
  remains available, but body and heading ink are now neutralized for legibility.
- Alternate named palettes remain selectable for compatibility; layout state
  colors are no longer dependent on a theme's accent being readable as body text.
