# Getting started

## Deploy the stack

Follow the Matrix initialization and HTTPS routing instructions in the product reference. Preserve generated keys and persistent data. Configure KyIdentity, its supported directory synchronization and exact application administrator roles before upgrading existing access mappings.

Sign in to the operator console and check Health for the configured Synapse, MAS, Element, database and optional LiveKit components. Then sign in to Element as an everyday user, join a test room and verify a message with a second participant.

Calling needs the documented HTTPS/WebSocket signaling routes and direct media ports; an HTTP tunnel alone does not carry media. Keep federation Closed until both operators have agreed and configured their allow-lists.
