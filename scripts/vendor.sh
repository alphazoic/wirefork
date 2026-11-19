#!/bin/sh
# Refresh vendor/ from go.mod and reapply wirefork's gVisor changes.
# Run after changing go.mod (e.g. a gVisor update); commit vendor/ afterwards.
set -e
cd "$(dirname "$0")/.."
rm -rf vendor
go mod vendor
chmod -R u+w vendor/gvisor.dev/gvisor
patch -d vendor/gvisor.dev/gvisor -p1 < patches/gvisor-tcp-recovery.patch
go build -o /dev/null ./cmd/wireproxy
echo "vendor/ refreshed and patched"
