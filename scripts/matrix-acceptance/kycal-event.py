# usage: python3 -I kycal-event.py DB CALENDAR UID
# Harness only (scripts/matrix-acceptance.sh, step suite-meetings): reads one stored event from
# the throwaway KyCalendar's database and prints two lines: the URL of its
# CONFERENCE;VALUE=URI;FEATURE=VIDEO;LABEL=KyMessages property (RFC 7986, parameters in any
# order) and the last line of its description, each "" when absent. Never part of a deployment.
import sqlite3
import sys

row = sqlite3.connect(sys.argv[1]).execute(
    "SELECT data FROM calendar_objects WHERE calendar_id = ? AND uid = ?", (sys.argv[2], sys.argv[3])
).fetchone()
data = row[0].decode() if isinstance(row[0], bytes) else row[0]
lines = data.replace("\r\n ", "").replace("\r\n\t", "").split("\r\n")
conference, description = "", ""
for line in lines:
    head, _, value = line.partition(":")
    name, *params = head.split(";")
    if name == "CONFERENCE" and {"VALUE=URI", "FEATURE=VIDEO", "LABEL=KyMessages"} == set(params):
        conference = value
    elif name == "DESCRIPTION":
        description = value.replace("\\n", "\n").replace("\\N", "\n").replace("\\,", ",").replace("\\;", ";").replace("\\\\", "\\")
print(conference)
print(description.rstrip("\n").split("\n")[-1])
