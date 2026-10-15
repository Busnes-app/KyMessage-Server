# usage: python3 -I verify-emails.py DB EXCEPT_USERNAME
# Harness only (scripts/matrix-acceptance.sh), against the throwaway KyIdentity's stopped
# database: marks every @kymatrix.test email but one user's as verified, as redeeming a mailed
# link would, and prints how many it marked. Never part of a deployment.
import sqlite3
import sys
from datetime import datetime, timezone

db = sqlite3.connect(sys.argv[1])
n = db.execute(
    "UPDATE users SET email_verified_at = ? WHERE email LIKE '%@kymatrix.test' AND username != ?",
    (datetime.now(timezone.utc).isoformat(sep=" "), sys.argv[2]),
).rowcount
db.commit()
print(n)
