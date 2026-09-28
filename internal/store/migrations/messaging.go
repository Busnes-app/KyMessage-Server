package migrations

const messagingRecoveryAuthSchema = `
CREATE TABLE messaging_recovery_auth (
    state_hash TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_hash TEXT NOT NULL REFERENCES sessions(token_hash) ON DELETE CASCADE,
    device_id TEXT NOT NULL REFERENCES messaging_devices(id) ON DELETE CASCADE,
    public_key TEXT NOT NULL,
    subject TEXT NOT NULL,
    registry_hash TEXT NOT NULL,
    sealed_request TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL
);
CREATE INDEX messaging_recovery_auth_user ON messaging_recovery_auth(user_id, expires_at);
`

// The first messaging schema stores public device identity and room ACLs only.
// Delivery is added separately so existing installations migrate incrementally.
const messagingSchema = `
CREATE TABLE messaging_devices (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    public_key TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK(status IN ('unverified', 'pending', 'approved', 'revoked')),
    challenge TEXT NOT NULL,
    enrollment_session TEXT NOT NULL,
    expires_at BIGINT NOT NULL,
    token_hash TEXT UNIQUE,
    approved_by TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    verified_at BIGINT
);
CREATE INDEX messaging_devices_user ON messaging_devices(user_id);

CREATE TABLE messaging_rooms (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at BIGINT NOT NULL
);
CREATE TABLE messaging_members (
    room_id TEXT NOT NULL REFERENCES messaging_rooms(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK(status IN ('invited', 'active', 'removed')),
    PRIMARY KEY(room_id, user_id)
);
CREATE INDEX messaging_members_user ON messaging_members(user_id, status);
`

const messagingDeliverySchema = `
ALTER TABLE messaging_rooms ADD COLUMN epoch BIGINT NOT NULL DEFAULT 0;
ALTER TABLE messaging_rooms ADD COLUMN sequence BIGINT NOT NULL DEFAULT 0;
ALTER TABLE messaging_rooms ADD COLUMN roster_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE messaging_rooms ADD COLUMN retained_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE messaging_members ADD COLUMN generation BIGINT NOT NULL DEFAULT 0;
CREATE TABLE messaging_events (
    room_id TEXT NOT NULL REFERENCES messaging_rooms(id) ON DELETE CASCADE,
    sequence BIGINT NOT NULL,
    device_id TEXT NOT NULL,
    event_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('application', 'commit')),
    epoch BIGINT NOT NULL,
    roster_hash TEXT NOT NULL,
    payload TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    PRIMARY KEY(room_id, sequence),
    UNIQUE(room_id, device_id, event_id)
);
CREATE TABLE messaging_epoch_devices (
    room_id TEXT NOT NULL REFERENCES messaging_rooms(id) ON DELETE CASCADE,
    device_id TEXT NOT NULL,
    generation BIGINT NOT NULL,
    joined_sequence BIGINT NOT NULL,
    PRIMARY KEY(room_id, device_id)
);
CREATE TABLE messaging_welcomes (
    room_id TEXT NOT NULL,
    sequence BIGINT NOT NULL,
    device_id TEXT NOT NULL,
    payload TEXT NOT NULL,
    PRIMARY KEY(room_id, sequence, device_id),
    FOREIGN KEY(room_id, sequence) REFERENCES messaging_events(room_id, sequence) ON DELETE CASCADE
);
`

// Device IDs intentionally have no cascading foreign key: deleting an account
// must not allow its previously published bytes to re-enter the pool.
const messagingKeyPackageSchema = `
CREATE TABLE messaging_key_packages (
    id TEXT PRIMARY KEY,
    device_id TEXT NOT NULL,
    payload TEXT NOT NULL,
    expires_at BIGINT NOT NULL,
    claim_room TEXT NOT NULL DEFAULT '',
    claimant_device TEXT NOT NULL DEFAULT '',
    claim_id TEXT NOT NULL DEFAULT '',
    member_generation BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX messaging_key_packages_available ON messaging_key_packages(device_id, expires_at);
CREATE UNIQUE INDEX messaging_key_packages_claim ON messaging_key_packages(claimant_device, claim_id) WHERE claim_id <> '';
`

const messagingIdentitySchema = `
CREATE TABLE messaging_identities (
    user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    generation BIGINT NOT NULL DEFAULT 1 CHECK(generation > 0)
);
INSERT INTO messaging_identities (user_id) SELECT DISTINCT user_id FROM messaging_devices;
ALTER TABLE messaging_devices ADD COLUMN identity_generation BIGINT NOT NULL DEFAULT 1;
ALTER TABLE messaging_rooms ADD COLUMN owner_identity_generation BIGINT NOT NULL DEFAULT 1;
ALTER TABLE messaging_members ADD COLUMN identity_generation BIGINT NOT NULL DEFAULT 1;
ALTER TABLE messaging_recovery_auth ADD COLUMN identity_generation BIGINT NOT NULL DEFAULT 1;
ALTER TABLE messaging_recovery_auth ADD COLUMN reset_confirmed INTEGER NOT NULL DEFAULT 0;
CREATE TABLE messaging_reset_receipts (
    state_hash TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_hash TEXT NOT NULL REFERENCES sessions(token_hash) ON DELETE CASCADE,
    device_id TEXT NOT NULL,
    identity_generation BIGINT NOT NULL,
    completed_at BIGINT NOT NULL
);
`

const messagingScopedPackageSchema = `
ALTER TABLE messaging_key_packages ADD COLUMN publication_room TEXT NOT NULL DEFAULT '';
CREATE INDEX messaging_key_packages_room ON messaging_key_packages(device_id, publication_room, expires_at);
`
