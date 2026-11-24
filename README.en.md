# Dreame Local Agent

[Русский](README.md)

A local service for rooted Dreame robot vacuums: control without the cloud, from a browser or from a smart home. Tested on the Dreame X50 Pioneer Edition (R2505), firmware 4.3.9_2021. The service does not provide a way to root the robot; it is installed on a robot that is already unlocked.

The agent is written in Go and runs **on the vacuum itself**. It calls the firmware's own `avacmd` and serves an HTTP API and a built-in web interface. No extra computer is needed.

## Features

- State; cleaning of the whole home, rooms, a zone or a spot; the station (dust emptying, mop washing and drying); suction and volume.
- Map: the saved map with rooms, the robot position and cleaned path while cleaning, merging and splitting rooms, thresholds, virtual walls, no-go and no-mop zones, per-room cleaning settings.
- Consumables with remaining life and reset, totals, do-not-disturb, cleaning history, readable error texts, every setting the robot reports.
- Detects the model and firmware by itself and shows only what the robot supports; on unknown firmware it only reads.
- Event stream, tokens with read or control rights, agent updates from the browser with automatic rollback.
- Modules installed from the web interface: [MQTT and Home Assistant](../dreame-module-mqtt), [region guard](../dreame-module-region).
- Voices: switching the built-in ones (EN, JA, KO, ZH), uploading your own voice packs, listening to phrases by number — [preparing a pack](docs/voice-packs.md) (Russian).
- Wi-Fi setup and a captive portal in access-point mode.

Map and station commands are verified on R2505; zone and spot cleaning, history and the robot path are so far covered by tests only.

## Install

You need ADB with root on the robot and an agent build (`sh build.sh`, see below). One command:

```sh
python3 tools/install_agent.py --serial ROBOT_IP:4567
```

Without a config file (`bridge/config.json` or the one given with `--config`) the installer reads the model, `did` and `mac` from the robot, generates a random API token and saves the config with mode 600. **Do not publish this file.** `--read-only` turns control off (state and map only). On reinstall, fields of the installed config the installer does not manage (extra tokens, `allow_unverified_firmware`) are kept. At the end the installer reports whether the firmware matches a checked profile and whether control is on.

The installer checks root, ARM64 and the robot identity, deploys the agent to `/data/dreame-local`, checks SHA-256 and the HTTP answer, and adds the agent start and the modules' `boot-env.sh` to the existing `/data/test/ava.sh` boot hook, keeping a backup. A firmware update renames `ava.sh` to `ava.sh_ok`: after it the agent and modules do not start until the hook is restored. `--no-boot-hook` installs without touching the hook.

Web interface: `http://ROBOT_IP:8765/`. Do not expose port 8765 to the internet: the interface is meant for a trusted LAN.

## API for adapters (`/api/v1`)

Every request carries `Authorization: Bearer <token>`; the answer is `{"result": ...}` or `{"error": ...}`.

- `GET /api/v1/state` — cached state. The agent polls the robot every 5 s and right after a command, so clients never load the robot. Fields: `activity` (`cleaning|paused|returning|docked|idle|error`), `status`/`status_name`, `battery`, `charging`, `suction`, `water`, `station`, `volume`, `error`/`error_name` (stable machine name) with `error_text`/`error_text_en` (the Dreame app's own text), `device_capability` (4/83 mask the robot reports), `region`, `region_code`, `cleaning_time` (min) and `cleaned_area` (m²) of the current or last run; once a minute also `consumables` (`{"filter": {"left": %, "hours": h}}`, only what the model reports), `stats` (`cleanings`, `minutes`, `area` m²) and `dnd` (`enabled`, `start`, `end`). Properties the robot did not answer are absent. `stale: true` means the last poll failed; `seq` grows on every change. Next to `state`, `command` is the last command the robot accepted (`name`, `at`, `outcome`): `pending` while waiting, `executed` once the state showed it (cleaning started, paused, stopped, heading to the dock, suction or volume changed), `unknown` if it did not within 30 s, `accepted` for commands with nothing visible (locate, resets, settings, station).
- `GET /api/v1/events` — the same snapshot as Server-Sent Events: the current one at once, then on every change (`id: <seq>`, `event: state`, `data: <snapshot>`), a keep-alive comment every 20 s, at most 8 streams.
- `GET /api/v1/capabilities` — `device_capability` decoded from 4/83 (`video_monitor`, `quick_command`, `streamer_and_ai`, `smart_charge`, `record_sound`); model and firmware, whether the firmware matches a checked profile (`firmware.verified`, with file hashes), the model's hardware table from the firmware itself (`/ava/conf/dconf_runtime.json`), readable properties and available commands.
- `POST /api/v1/command` — `{"name": "start|pause|stop|dock|locate|mapping|empty|wash_mop|dry_start|dry_stop"}`, `{"name": "set_suction|set_volume", "value": N}`, `{"name": "clean_rooms", "rooms": [ids], "repeats": 1..3, "suction": 0..3, "water": 1..3}`, `{"name": "clean_zone", "zones": [[x0, y0, x1, y1]], ...}` (up to 5 rectangles in map millimetres, at least 30 cm), `{"name": "clean_spot", "points": [[x, y]], ...}` (up to 3), `{"name": "reset_consumable", "consumable": "filter"}`, `{"name": "set_dnd", "enabled": true, "start": "22:00", "end": "08:00"}`, `{"name": "set_schedule", "id": N, "enabled": true, "time": "08:30", "days": "0111110", "rooms": [ids], "repeats", "suction", "water"}` (days are seven 0/1 digits from Sunday, `0000000` runs once; no `id` adds one, no `rooms` cleans the whole map, editing without `rooms` keeps what the task cleans; the list is `schedules` in the state), `{"name": "delete_schedule", "id": N}`, `{"name": "go_to", "points": [[x, y]]}` (drive to a point for the camera, mode 23, the robot waits there), `{"name": "patrol", "points": [[x, y], ...]}` (up to 20 points: the agent sends `go_to` in turn, waits 5 s at each and docks; any motion command ends it), `{"name": "dock_self_repair"}` (station self-repair as in the app: 99/8 `{"bittest":[17,0]}`, docked only, 5-10 minutes, status 15), `{"name": "remote_move", "velocity": -600..600, "rotation": -360..360}` (manual drive, a short step per command: the first one takes the robot off the dock, status 13 `remote_control`, it stops ~15 s after the last step). Supports `X-Dreame-Request-ID`; a non-zero robot code is returned as an error.
- `GET /api/v1/map.png` — picture of the saved map (rooms, station; while cleaning also the robot, its path, the mopped floor and the zone being cleaned).
- `GET /api/v1/map` — calibration of that picture (`calibration_points`: three robot-millimetre to pixel points, the xiaomi-vacuum-map-card format) and the rooms with their centres in millimetres.
- `GET /api/v1/settings` — the panel's settings this robot has, with current values; change one with the command `{"name":"set_setting","setting":"<id>","to":<value>}`.
- `GET /api/v1/voice` — the robot's voices (`current`, `voices`: `id`, `builtin`, `files`); `GET /api/v1/voice/phrases?id=` — a voice's phrase numbers, `GET /api/v1/voice/phrase?id=&n=` — the phrase file (`audio/ogg`); `POST /api/v1/voice/install?id=<code>` with the `.tar.gz` pack as the body, `POST /api/v1/voice/use`, `/remove` with `{"id"}`, `/play` with `{"number"}` (say a phrase on the robot). Main token only; preparing a pack and the phrase numbers: [docs/voice-packs.md](docs/voice-packs.md) (Russian; agent 0.27+).
- `GET /api/v1/logs` — the last 32 KB of the logs: the agent (starts and failed requests, 5xx), the agent update and each module (`source`, `text`, `size`). Main token only (agent 0.28+).
- Obstacles found by the AI camera: `local.map_view` returns `obstacles` (`[x, y, type, confidence %]`, robot millimetres) and `obstacles_at`, the time of the run that found them. The panel draws them as icons, `map.png` for Home Assistant as dots (red: wires, socks and the like). Agent 0.29+.
- `GET /api/v1/history` — cleaning history, newest first: `start`, `end`, `mode`, `result` (`docked`/`idle`/`error`), `error`, `minutes`, `area`. The firmware keeps these records for the cloud only, so the agent records runs itself; time and area are the change of the robot's lifetime totals, and runs while the agent is not running are missing.

`/rpc` serves the web interface and debugging: MIOT properties (`get_properties`, `set_properties`, `action`) and local methods — `local.map_view`, `local.live_pose`, `local.thresholds`, `local.restrictions`, `local.room_settings`, `local.room_order` (`{"map_id", "revision", "rooms": [every room in order]}`), `local.room_types`, `local.map_backup_status` (map backups, restore state 6/11, saved maps), `local.map_backup`, `local.map_restore` / `local.map_backup_delete` (`{"name"}`), `local.map_use` (`{"map_id"}`), `local.carpets` (`{"map_id", "revision", "carpets_added": [[x0, y0, x1, y1]], "carpets_ignored": [...]}`, both lists in full), `local.furniture` (`{"map_id", "revision", "remove": [index], "add": [{"type": 1..25, "x", "y", "width", "height", "rotation"}]}`; the agent sends the whole list), `local.messages_status` / `local.messages_clear` (the agent's message feed), `local.setting`, `local.wifi_status`, `local.wifi_setup`, `local.reboot`.

### Unknown firmware

The agent compares firmware files with checked profiles. On firmware without a profile, commands, setting writes and map edits are off: the agent only reads. To control such a robot at your own risk, set `"allow_unverified_firmware": true` in the config. Agent updates, modules, Wi-Fi and reboot work either way. To add a profile, download the compatibility report in "Diagnostics" and send it: it has the model, firmware and its file hashes, without the robot id.

### Tokens with rights

The main `token` from the config has full access. For a smart home or other devices create a separate token in the web interface ("Access") or with `POST /api/v1/system/tokens` `{"name": "ha", "scope": "read|control"}`. The token is shown once; the config keeps only its SHA-256. List: `GET /api/v1/system/tokens`; revoke: `DELETE /api/v1/system/tokens/<name>`; only the main token manages tokens.

| Scope | Allowed |
|---|---|
| `read` | `GET state`, `capabilities`, `events`, `history`, `map.png`; reads through `/rpc` |
| `control` | the same plus `POST /api/v1/command` |
| main token | everything, including modules, agent updates, settings, Wi-Fi, reboot and writes through `/rpc` |

## Modules

A module is a `.tar.gz` package installed in the web interface: "Modules" → "Install module" → "Check package" → "Install". Before installing, the page shows the manifest, files, SHA-256 and the rights the module asks for. There are no signatures: a module runs on the robot as root, install only from a source you trust.

The package is unpacked to `/data/dreame-local/modules/<name>/`:

- `manifest.json` — `name` (`a-z0-9_-`), `version`, `title`, optional `description`, `handler`, `panel`, `read_actions`, `firmware_messages`, `control`, `service`;
- `handler` — an executable run as `handler <action>`: JSON parameters on stdin, scalar ones also as `P_<key>` variables; JSON answer on stdout, a non-zero exit and `{"error": "..."}` on failure. Environment: `DREAME_MODULE_DIR` (replaced as a whole on update), `DREAME_MODULE_DATA` (`/data/dreame-local/module-data/<name>`, kept on update, removed with the module), `DREAME_API`, `DREAME_MODULE_TOKEN`. Lifecycle actions: `check` (before install; a failure cancels it), `install` (after copying; a failure restores the previous version; on update `DREAME_MODULE_PREVIOUS` points to the old version's directory), `uninstall`. Other actions are `POST /api/v1/m/<name>/<action>`;
- `panel` — an ES module `export default function mount(element, ctx)` with `ctx.read/ctx.call(action, params)`, `ctx.rpc(method, params)`, `ctx.onState(fn)`, `ctx.toast(text, error)`;
- `service` — an executable the agent keeps running as `<service> run` with the same environment: restarted on failure (2 s to 1 min back-off), stopped with SIGTERM on update, removal and agent exit; output goes to `service.log` in the data directory;
- `boot-env.sh` — sourced from the boot hook before the firmware's `ava` starts (for example to preload a library); takes effect after a reboot.

The module token is derived from the main token: it survives agent restarts and stops working when the module is removed or the main token changes. It allows `GET` state, capabilities, events, history and the map picture, and `POST /api/v1/firmware_message` with a `cmd` declared in `firmware_messages`; with `"control": true` also `POST /api/v1/command` and other modules' `read_actions` (`POST /api/v1/m/<module>/<action>`, agent 0.26+).

## Agent updates

A new version is installed in the web interface: "Agent update" → choose the file → "Check file" → "Update". The agent accepts only a Linux executable for the robot's architecture that answers `--version` with `dreame-local-agent <version>`. A helper process stops the agent, installs the new file, keeps the old one as `dreame-local-agent.previous` and waits up to 45 s for the new version to answer; otherwise it restores the old one. "Restore version …" installs `.previous` the same way. Only the binary and its built-in web interface are updated.

## Build and tests

Go 1.24 or newer. `sh build.sh` builds `dist/dreame-local-agent-<version>-linux-arm64` with its `.sha256` (version from `agent/main.go`). On GitHub, `.github/workflows/ci.yml` runs the tests on every push and publishes a release for a `v<version>` tag.

```sh
cd agent && go vet ./... && go test ./...
cd .. && python3 -m unittest discover -s tests -p 'test_*.py'
node tests/test_web_ui.cjs && node tests/test_command_results.cjs   # need Playwright
```

Go tests are Linux-only (`Setsid`, a Python mock of `avacmd`); on Windows build the test binary with `GOOS=linux go test -c` and run it in WSL.

## Contents

- `agent/` — the Go service, built-in web interface and shell scripts.
- `tools/install_agent.py` — ADB installer with identity checks.
- `tests/` — web interface and installer tests.

Attribution is in `NOTICE.md` and the licence files. MIT licence.
