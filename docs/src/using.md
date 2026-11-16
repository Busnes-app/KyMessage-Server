# Everyday tasks

## Chat and calls

Use Element to create or join rooms and exchange messages. Verify the room's encryption state. The console cannot decrypt encrypted messages, but Synapse does not enforce that every room is encrypted. A browser may ask for microphone/camera permission when joining a call.

## Operator console

Overview and Health show component state. Users manages sessions. Rooms lists metadata and membership; closing a room prevents rejoining and preserves history, while permanent deletion purges this server's history. Deleting a room does not remove every media object or another server's copy. Read the confirmation before either action.

## Federation and agents

Federation is closed by default. Both servers must allow each other, and Synapse needs the restart shown in settings. Agent accounts are created deliberately and owned by a member. An agent decrypts messages it reads and may send them to a hosted model; room encryption does not prevent that disclosure.

## Recovery

Protect both the sealed server capsule and Matrix media backups. The backup screen's Run now action seals the capsule only; use the documented full recovery path for media.
