# Attribution

The on-device Go agent in `agent/` is an independent implementation using the Go standard library. It invokes the robot firmware's `avacmd` executable and reads the firmware saved-map file. Firmware executables are not distributed here.

The settings identifiers, labels, and option metadata in `agent/ui/settings.js` were derived from the MIT-licensed Tasshack/dreame-vacuum Home Assistant project; see `LICENSE-Tasshack`. The charging-status property (3/2) meaning in `agent/device.go` comes from the same project and was confirmed on an r2505. Per-model hardware capabilities are read at runtime from the robot's own firmware file and are not distributed here. The UI, transport logic, and map editor are original changes.

The regional-lock interceptor is no longer part of this repository; it is a separately distributed module.

Original agent, helper, and test code is provided under the MIT license in `LICENSE`. No Valetudo source, binary, or fixture is included.
