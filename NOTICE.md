# Attribution

The on-device Go agent in `agent/` is an independent implementation using the Go standard library. The optional C regional-lock interceptor uses the robot system libc/libdl. It invokes the robot firmware's `avacmd` executable and reads the firmware saved-map file. Firmware executables are not distributed here.

The settings identifiers, labels, and option metadata in `agent/ui/settings.js` were derived from the MIT-licensed Tasshack/dreame-vacuum Home Assistant project; see `LICENSE-Tasshack`. The UI, transport logic, and map editor are original changes.

Original agent, helper, and test code is provided under the MIT license in `LICENSE`. No Valetudo source, binary, or fixture is included.
