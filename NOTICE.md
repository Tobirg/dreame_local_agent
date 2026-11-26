# Attribution

The on-device Go agent in `agent/` is an independent implementation using the Go standard library. It invokes the robot firmware's `avacmd` executable and reads the firmware saved-map file. Firmware executables are not distributed here.

Property and action IDs, setting keys, consumables and error texts are checked against the Dreame app's own robot plugins (`tools/import_plugin_spec.py`, generated `agent/official.go`; entries in `agent/ui/settings.js` carry `"src":"plugin"`). The MIT-licensed Tasshack/dreame-vacuum Home Assistant project (see `LICENSE-Tasshack`) is used only where the app has no data: settings marked `"src":"tasshack"`, option metadata, the stable `error_name` identifiers and three consumables (17, 32, 33). The charging-status property (3/2) meaning in `agent/device.go` comes from the same project and was confirmed on an r2505. Per-model hardware capabilities are read at runtime from the robot's own firmware file and are not distributed here. The UI, transport logic, and map editor are original changes.

The regional-lock interceptor is no longer part of this repository; it is a separately distributed module.

Original agent, helper, and test code is provided under the MIT license in `LICENSE`. No Valetudo source, binary, or fixture is included.
