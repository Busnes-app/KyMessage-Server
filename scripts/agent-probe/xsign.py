"""Cross-signing bootstrap over raw HTTP, as a recovery-key bootstrap does it.

Usage: python -I xsign.py <homeserver URL> <user ID> <device ID>; token in $PROBE_TOKEN.
Prints one JSON line per step: {"step", "status", "body"}.
"""
import base64
import json
import os
import sys
import urllib.error
import urllib.request

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

base, user, device = sys.argv[1:4]
token = os.environ["PROBE_TOKEN"]


def b64(b):
    return base64.b64encode(b).decode().rstrip("=")


def canon(o):
    return json.dumps(o, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()


class Key:
    def __init__(self):
        self.k = Ed25519PrivateKey.generate()
        self.pub = b64(self.k.public_key().public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw))

    def sign(self, obj, key_id):
        o = {k: v for k, v in obj.items() if k not in ("signatures", "unsigned")}
        obj.setdefault("signatures", {}).setdefault(user, {})["ed25519:" + key_id] = b64(self.k.sign(canon(o)))
        return obj


def call(step, path, body):
    req = urllib.request.Request(base + path, method="POST", data=canon(body),
                                 headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            st, raw = r.status, r.read()
    except urllib.error.HTTPError as e:
        st, raw = e.code, e.read()
    print(json.dumps({"step": step, "status": st, "body": json.loads(raw or b"{}")}), flush=True)
    return st


def cross_keys():
    master, ssk, usk = Key(), Key(), Key()
    def k(usage, key):
        return {"user_id": user, "usage": [usage], "keys": {"ed25519:" + key.pub: key.pub}}
    body = {"master_key": k("master", master),
            "self_signing_key": master.sign(k("self_signing", ssk), master.pub),
            "user_signing_key": master.sign(k("user_signing", usk), master.pub)}
    return body, ssk


dev = Key()
dk = {"user_id": user, "device_id": device,
      "algorithms": ["m.olm.v1.curve25519-aes-sha2", "m.megolm.v1.aes-sha2"],
      "keys": {"ed25519:" + device: dev.pub, "curve25519:" + device: b64(os.urandom(32))}}
dev.sign(dk, device)
call("device_keys_upload", "/_matrix/client/v3/keys/upload", {"device_keys": dk})

first, ssk = cross_keys()
call("xsign_first_upload", "/_matrix/client/v3/keys/device_signing/upload", first)
call("xsign_same_keys_again", "/_matrix/client/v3/keys/device_signing/upload", first)
signed = ssk.sign({k: v for k, v in dk.items() if k != "signatures"}, ssk.pub)
signed["signatures"][user].update(dk["signatures"][user])
call("device_signature_upload", "/_matrix/client/v3/keys/signatures/upload", {user: {device: signed}})
second, _ = cross_keys()
call("xsign_replace", "/_matrix/client/v3/keys/device_signing/upload", second)
