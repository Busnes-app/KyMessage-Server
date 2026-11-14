# Evidence: calls in rooms of three or more, 2026-11-14

Branch `fix/group-calls`. Owner decision 2026-11-14: turn Element's `UIFeature.widgets` on so
group rooms and KyCalendar meeting rooms get a call button; integrations stay off; Element
stays stock. Source citations are Element Web `v1.12.30` (commit `f19cfd9`), the image
`docker-compose.matrix.yml` pins, paths under `apps/web/src/`.

## Cause

`hooks/room/useRoomCall.ts:346`:
`if ((memberCount > 2 && !widgetsFeatureEnabled) || !voipFeatureEnabled || roomDoesNotExist)`
hides both call buttons. `widgetsFeatureEnabled` is `UIFeature.widgets` (line 113). No other
setting enters that condition, so nothing narrower than `UIFeature.widgets` shows the buttons.
`UIFeature.widgets` is a config-only setting (`settings/Settings.tsx:1438`, `LEVELS_UI_FEATURE`):
users cannot change it.

With `element_call.use_exclusively` and no Jitsi widget, the only call option is Element Call
(`useRoomCall.ts:181`), so no Jitsi choice appears. Calls off (`KY_MATRIX_CALLS=off`) keeps
`UIFeature.widgets` off: `matrix-init` renders it as `not CallsOff`, like `UIFeature.voip`.

## What `UIFeature.widgets` gates in v1.12.30

The four uses outside the call hook (`grep UIFeature.Widgets`):

| Where | What it shows | Reachable by an ordinary member? |
|---|---|---|
| `components/views/rooms/AuxPanel.tsx:40` | the apps drawer: room widgets pinned to the top render above the timeline | Only widgets someone with the room's widget power level added (below) |
| `slash-commands/SlashCommands.tsx:539` | `/addwidget <url>` | Runs, but `WidgetUtils.canUserModifyWidgets` (power levels) refuses: "You cannot modify widgets in this room." Proven on the stack (bob, meeting room) |
| `settings/Settings.tsx:697` | the sticker button's controller | Hidden: `setting_defaults` sets `MessageComposerInput.showStickersButton` false. A user can turn it on for themselves; with no integration manager it opens only the "no integration manager" dialog |
| `components/views/settings/SetIntegrationManager.tsx:66` | the integration-manager toggle in Security settings | Shows, has nothing to open: `integrations_ui_url`/`_rest_url` null, `integrations_widgets_urls` empty, and `/.well-known/matrix/client` (`internal/api/wellknown.go`) serves no `m.integrations` |

Not gated by it, so already reachable with the old config: the room info **Extensions** panel
(`components/views/right_panel/RoomSummaryCardView.tsx:273`, `ExtensionsCard.tsx`), which lists
the room's widgets and opens one in the right panel. Turning the setting on adds the drawer and
`/addwidget`, not the ability to see a widget.

## Who can add a widget

A room widget is an `im.vector.modular.widgets` state event. Synapse enforces the room's power
levels for it whichever client sends it:

- Element's group rooms (`createRoom.ts:199`, `private_chat` preset) leave it at Synapse's
  `state_default` 50: the creator and moderators may, a member at 0 may not. Proven on the stack:
  bob's `sendStateEvent` into the `prove` group room answers `M_FORBIDDEN`.
- DMs (`trusted_private_chat`) make both people creators: either may add one, in a room of two.
- KyCalendar meeting rooms (`internal/synapseadmin/meetings.go`) now set it to 100: only the
  console, which adds none. The organizer (50) and everyone else are refused. Proven: alice's
  and bob's attempts answer `M_FORBIDDEN`, and Synapse holds no widget event after the run.
  Rooms created before this change keep 50 (organizer only).

So widgets are limited to what the room's power levels already allow its admins and
moderators, which is also what other Matrix clients could already do without Element.

## What a widget added by one member can do to another

From `components/views/elements/AppTile.tsx` and `stores/widgets/ElementWidgetDriver.ts`:

- **Loading**: a room widget someone else added does not load until the viewer accepts
  Element's "continue" prompt for that widget (`AppTile.tsx:200`, `allowedWidgets` per event);
  the prompt names the widget's domain. Until then no request goes to it. Exception: a widget of
  type `jitsi` always loads Element's own local Jitsi wrapper (`AppTile.tsx:624`), which connects
  to the widget's conference domain only after the viewer presses Join.
- **Capabilities**: only screenshots (the widget offering one; it cannot take one of Element) and
  `requires_client` are granted unasked (`ElementWidgetDriver.ts:103`); reading or sending room
  events, always-on-screen and navigation ask the viewer (`WidgetCapabilitiesPromptDialog`).
  `navigate` accepts Matrix permalinks only (`navigateToPermalink`).
- **OpenID**: `askOpenID` asks the viewer unless remembered (`ElementWidgetDriver.ts:643`); the
  token proves the user's Matrix ID to the widget, nothing more.
- **Browser permissions**: once accepted, the iframe is delegated `microphone; camera;
  display-capture; clipboard-*` (`AppTile.tsx:73`). Browsers attribute an iframe's request to
  Element's origin, so a user who let Element use the camera for calls and chose "remember"
  gives an accepted widget the camera without a new prompt. This is the residual risk; it
  needs the user to accept a widget that a room admin or moderator added, and it was already
  reachable through the Extensions panel.
- **CSP**: Element's `index.html` carries `frame-src * blob: data:`. Its stock nginx adds no
  header and this repo does not serve Element, so a narrower `frame-src` would be the
  operator's reverse proxy's job; not shipped.

Left unset on purpose: `integrations_*` (stay null/empty), `widget_build_url` (unset: no
integration manager builds video-room widgets).

## Running stack (CI `matrix-acceptance`, first leg, step `meetings`)

The step now also asserts Element serves `UIFeature.widgets` true with no integration manager,
the meeting room's widget level is 100, alice's and bob's widget state events and bob's
`/addwidget` are refused, bob's widget state event in the two-person group room is refused,
no widget event exists in Synapse, the room has three joined members, and alice starts a video
call from Element's Video call button that bob joins, with audio and decoded video received
by both. Screenshots are uploaded as the `matrix-acceptance-evidence-0` artifact.

Run 43616239513 (head `89c09c9`, GitHub Actions), every check above passed (`PASS meetings 19s`):

- Before: [2026-11-meeting-rooms/stack/meeting-alice.png](2026-11-meeting-rooms/stack/meeting-alice.png),
  no call button in the meeting room.
- After: [meeting-call-button-alice.png](2026-11-group-calls/meeting-call-button-alice.png),
  Video call in the header of the same room with 3 members;
  [meeting-call-alice.png](2026-11-group-calls/meeting-call-alice.png) and
  [meeting-call-bob.png](2026-11-group-calls/meeting-call-bob.png), the call with both
  participants' video.

The harness captures 1280 px light only; phone width and dark theme are not captured (Element's
header is stock and unchanged apart from the button).
