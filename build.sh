#!/bin/sh
# Release build of the agent for the robot (linux/arm64):
#   dist/dreame-local-agent-<version>-linux-arm64 and its .sha256
# The version comes from agent/main.go unless given as the first argument.
set -eu
cd "$(dirname "$0")"
GO=${GO:-go}
VERSION=${1:-$(sed -n 's/^var version = "\(.*\)"$/\1/p' agent/main.go)}
NAME=dreame-local-agent-$VERSION-linux-arm64
mkdir -p dist
(cd agent && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 "$GO" build -trimpath -buildvcs=false \
	-ldflags "-s -w -buildid= -X main.version=$VERSION" -o "../dist/$NAME" .)
(cd dist && sha256sum "$NAME" > "$NAME.sha256" && cat "$NAME.sha256")
