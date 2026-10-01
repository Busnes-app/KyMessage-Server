# KyMessages protocol findings

Research checked 2026-09-27. This is evidence for product planning, not a claim that these capabilities are implemented. Standards establish protocol properties; deployment compatibility still needs executable tests. Moving `stable` documentation and development branches must be pinned to versions before implementation.

## OCM and Nextcloud Talk interoperability

**Established:** The current OCM development specification includes capability-negotiated RFC 9421 HTTP Message Signatures and JWKS discovery through `jwksUri`; unsigned interoperability can remain possible under specified policies. This is version-sensitive, not evidence that all deployed OCM peers support identical signatures. [OCM draft, HTTP Message Signatures](https://github.com/cs3org/OCM-API/blob/develop/IETF-OCM.md)

**Established:** Nextcloud's documentation currently labeled version 35 describes outgoing signed OCM requests and published JWKS. This corroborates the direction, but does not establish compatibility with older Nextcloud installations. [Nextcloud OCM administration](https://docs.nextcloud.com/server/stable/admin_manual/occ_ocm.html)

**Established:** Talk has federation-capable chat operations under `/ocs/v2.php/apps/spreed/api/v1/chat/{token}`. The published interface carries message strings, reactions and editing operations; it does not specify the proposed KyMessages MLS profile. [Talk Chat API](https://nextcloud-talk.readthedocs.io/en/stable/chat/)

**Established:** Nextcloud's public E2EE FAQ describes encryption for audio and video. It does not establish MLS text compatibility. [Nextcloud E2EE FAQ](https://portal.nextcloud.com/article/Nextcloud-Talk/Frequently-Asked-Questions/End-To-End-Encryption)

**Product implication (inference):** Implementing `/ocm/shares` and `/ocm/notifications` does not prove end-to-end Talk messaging compatibility. Keep KyMessages-to-KyMessages federation and Talk compatibility as separate milestones. Do not advertise transparent MLS-preserving Talk interoperation without a shared cryptographic/application profile and tests against pinned releases. A bridge that decrypts text becomes an endpoint with plaintext access and changes the trust model; it cannot silently join a room sold as blind to servers.

## Calling, SFrame and connectivity

**Established:** SFrame protects media content while permitting SFU forwarding metadata. It requires application-supplied key management. RFC 9605 §5.2 describes MLS exporter integration; §9.2 leaves key assignment and rotation policy to the application. Removing receivers requires appropriately fresh keys, not merely removing a routing entry. [RFC 9605, §§5, 6 and 9](https://www.rfc-editor.org/rfc/rfc9605.html)

**Established:** W3C Encoded Transform defines media-processing APIs, including worker transforms and SFrame-related interfaces. A standards document is not proof that every required browser implements the same API or media combination. [WebRTC Encoded Transform](https://www.w3.org/TR/webrtc-encoded-transform/)

**Established:** WebRTC uses DTLS-SRTP, but the security architecture separately addresses signaling, endpoint identity and fingerprint validation. Transport encryption alone does not establish that the peer is the intended person when signaling can be substituted. [RFC 8827, §§6–7](https://www.rfc-editor.org/rfc/rfc8827.html)

**Established:** Restrictive NAT/firewalls can require TURN, including when an SFU is publicly reachable. TURN over port 443 can be necessary for restrictive networks; reachability and relay configuration must be tested. [Nextcloud TURN deployment guide](https://nextcloud-talk.readthedocs.io/en/stable/TURN/)

**Product implications (inference):** Promise one-container text deployment first. Calling additionally needs tested UDP/TCP/TLS relay paths, credentials, bandwidth budgets and operator instructions. Bind call identity to approved messaging devices. Treat SFrame library choice, browser/native compatibility, packetization, screen audio and SFU congestion behavior as feasibility gates. Encryption failure must stop media, not fall back silently to unprotected SFU content.
