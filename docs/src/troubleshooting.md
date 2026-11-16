# Troubleshooting

- **Sign-in works but chat access fails:** check local directory/MAS status, application assignment and the user's room membership.
- **Calls connect without media:** verify TCP/UDP media routing, firewall and advertised media address; HTTPS signaling is only part of the path.
- **Federation reports Not allowed yet:** check both allow-lists and whether Synapse was restarted.
- **Directory status is stale:** inspect the console's KyIdentity sync status and the pending delivery at KyIdentity.
- **Missing attachments after restore:** follow the media restore steps as well as capsule recovery.
- **Room removal:** distinguish close from permanent delete and check the scope before confirming.

When reporting a problem, include the product version, platform, failed task and a redacted error. Exclude pairing links, device secrets, recovery codes, custodian shares and private content.
