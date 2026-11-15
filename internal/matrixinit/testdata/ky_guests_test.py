# Unit test of the guest confinement rules (templates/ky_guests.py.tmpl) against stand-ins for
# Synapse's module API. TestGuestConfinementRules runs it: python3 -I -B ky_guests_test.py MODULE.
# The live proof is spike S3 in scripts/matrix-acceptance.sh.
import asyncio
import importlib.machinery
import importlib.util
import sys
import types

errors = types.ModuleType("synapse.api.errors")
errors.Codes = types.SimpleNamespace(FORBIDDEN="M_FORBIDDEN")
module_api = types.ModuleType("synapse.module_api")
module_api.NOT_SPAM = "NOT_SPAM"
module_api.ModuleApi = object
for name, mod in {"synapse": types.ModuleType("synapse"), "synapse.api": types.ModuleType("synapse.api"),
                  "synapse.api.errors": errors, "synapse.module_api": module_api}.items():
    sys.modules[name] = mod
spec = importlib.util.spec_from_loader("ky_guests", importlib.machinery.SourceFileLoader("ky_guests", sys.argv[1]))
ky = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ky)

CONSOLE = "@kymessages-console:ex.test"
G, OTHER_G, ALICE = "@guest-abc:ex.test", "@guest-xyz:ex.test", "@alice:ex.test"
MEETING, OTHER = "!meeting:ex.test", "!other:ex.test"


class Ev:
    def __init__(self, type, sender, state_key=None, content=None):
        self.type, self.sender, self.state_key, self.content = type, sender, state_key, content or {}

    def is_state(self):
        return self.state_key is not None

    @property
    def membership(self):
        return self.content.get("membership")


def marker(localpart, sender=CONSOLE):
    return {(ky.MARKER, localpart): Ev(ky.MARKER, sender, localpart)}


ROOMS = {MEETING: marker("guest-abc"), OTHER: marker("guest-abc", sender=ALICE)}


class Api:
    def __init__(self):
        self.cb = {}

    def is_mine(self, user_id):
        return user_id.endswith(":ex.test")

    def register_spam_checker_callbacks(self, **kw):
        self.cb.update(kw)

    register_media_repository_callbacks = register_third_party_rules_callbacks = register_spam_checker_callbacks

    async def get_room_state(self, room_id, keys):
        return {k: v for k, v in ROOMS.get(room_id, {}).items() if k in keys}


api = Api()
ky.GuestConfinement(ky.GuestConfinement.parse_config({"console": CONSOLE}), api)
cb = api.cb
run = asyncio.run
failed = []


def check(got, want, what):
    if got != want:
        failed.append(f"{what}: got {got!r}, want {want!r}")


F, OK = "M_FORBIDDEN", "NOT_SPAM"
check(run(cb["user_may_create_room"](G, {})), F, "guest creates a room")
check(run(cb["user_may_create_room"](ALICE, {})), OK, "member creates a room")
check(run(cb["user_may_create_room"]("@guest-abc:elsewhere.test", {})), OK, "a federated guest- name is not ours")
check(run(cb["user_may_invite"](G, ALICE, MEETING)), F, "guest invites")
check(run(cb["user_may_invite"](ALICE, G, MEETING)), OK, "member admits the guest into its meeting")
check(run(cb["user_may_invite"](ALICE, OTHER_G, MEETING)), F, "member invites another meeting's guest")
check(run(cb["user_may_invite"](ALICE, G, OTHER)), F, "marker not sent by the console")
check(run(cb["user_may_invite"](ALICE, G, "!dm:ex.test")), F, "member invites the guest to a DM")
check(run(cb["user_may_send_3pid_invite"](G, "email", "a@b", MEETING)), F, "guest 3pid invite")
check(run(cb["user_may_join_room"](G, MEETING, False)), F, "guest joins uninvited")
check(run(cb["user_may_join_room"](G, MEETING, True)), OK, "guest joins once admitted")
check(run(cb["user_may_join_room"](G, OTHER, True)), F, "guest joins an unmarked room")
check(run(cb["user_may_create_room_alias"](G, "#a:ex.test")), F, "guest alias")
check(run(cb["user_may_publish_room"](G, MEETING)), F, "guest publishes")
check(run(cb["check_username_for_spam"]({}, G)), True, "guest directory search hidden")
check(run(cb["check_username_for_spam"]({}, ALICE)), False, "member directory search")
check(run(cb["is_user_allowed_to_upload_media_of_size"](G, 1)), False, "guest upload")
check(run(cb["is_user_allowed_to_upload_media_of_size"](ALICE, 1)), True, "member upload")

allowed = lambda ev, state=ROOMS[MEETING]: run(cb["check_event_allowed"](ev, state))[0]
member = lambda m, name=None, key=G: Ev("m.room.member", G, key, {"membership": m, **({"displayname": name} if name else {})})
check(allowed(Ev("m.room.create", G, "", {})), False, "guest creates a room (event)")
check(allowed(member("knock", "Bob (guest)")), True, "guest knocks on its meeting")
check(allowed(member("knock")), True, "guest knocks without a profile name")
check(allowed(member("knock", "Bob (guest)"), ROOMS[OTHER]), False, "guest knocks on an unmarked room")
check(allowed(member("knock", "Bob (guest)"), {}), False, "guest knocks on another room")
check(allowed(member("join", "Bob (guest)")), True, "guest joins with the suffix")
check(allowed(member("join", "The boss")), False, "guest drops the suffix")
check(allowed(member("join")), False, "guest joins without a name")
check(allowed(member("invite", "x (guest)", ALICE)), False, "guest invites through an event")
check(allowed(member("leave"), {}), True, "guest leaves anywhere")
check(allowed(Ev("m.room.message", G, None, {"body": "hi"})), True, "guest talks in its meeting")
check(allowed(Ev("m.room.message", G, None, {"body": "hi"}), {}), False, "guest talks elsewhere")
check(allowed(Ev("m.room.topic", G, "", {"topic": "x"})), False, "guest sends other state")
check(allowed(Ev("org.matrix.msc3401.call.member", G, "_@guest-abc:ex.test_D", {})), True, "guest joins the call")
check(allowed(Ev("m.room.message", ALICE, None, {}), {}), True, "members are untouched")
try:
    ky.GuestConfinement.parse_config({})
    failed.append("parse_config accepted a config without console")
except ValueError:
    pass
if failed:
    print("\n".join(failed))
    sys.exit(1)
print("ok")
