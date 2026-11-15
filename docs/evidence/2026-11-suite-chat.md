# Chat from KyPost and KyCalendar: console evidence

Settings → Server before (`origin/master` 8fe957a) and after (`feat/suite-chat`), 1280 and 390
px, light and dark: `2026-11-suite-chat/before/`, `2026-11-suite-chat/after/`. A throwaway
server with Matrix configured (MAS unreachable) and both pairs from the environment
(`KY_KYCALENDAR_*`, `KY_KYPOST_*`). The KyPost row is new after KyCalendar's; KyCalendar's
line now also says attendees can open a chat. The sync and federation errors come from the
throwaway server having no MAS or Synapse behind it. Contract: [SUITE-CHAT.md](../SUITE-CHAT.md).
