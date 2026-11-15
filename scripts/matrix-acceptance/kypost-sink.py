"""usage: kypost-sink.py SENT_JSONL SECRET_HEX_FILE

What KyCalendar handed the KyPost stand-in (overrides/nginx.conf), one TSV line per message:
method, recipients (comma-separated), and the guest links in that recipient's copy
(space-separated, "-" for none). Exits non-zero unless every message is a kycalendar.itip.send
signed with syncauth v1 under the pair secret KyIdentity issued for kycalendar and kypost.
"""
import hashlib
import hmac
import json
import re
import sys

secret = bytes.fromhex(open(sys.argv[2]).read().strip())
for line in open(sys.argv[1], encoding="utf-8"):
    r = json.loads(line)
    body = r["body"].encode()
    base = "\n".join(["ky-sync-v1", r["ts"], r["type"], r["id"], hashlib.sha256(body).hexdigest()])
    want = "v1=" + hmac.new(secret, base.encode(), hashlib.sha256).hexdigest()
    if r["type"] != "kycalendar.itip.send" or not hmac.compare_digest(want, r["sig"]):
        sys.exit(f"kypost-sink: message {r['id']} is not signed as kycalendar.itip.send under the pair secret")
    m = json.loads(body)
    ics = re.sub(r"\r?\n[ \t]", "", m["ics"])  # unfold
    links = re.findall(r"https://admin\.kymatrix\.test/meet#[A-Za-z0-9_-]+", ics)
    print(m["method"], ",".join(m["recipients"]), " ".join(links) or "-", sep="\t")
