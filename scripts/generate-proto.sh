#!/usr/bin/env sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
PATH="$(go env GOPATH)/bin:$PATH"

rm -rf "$ROOT/services/mailbox-api/pb"
mkdir -p "$ROOT/services/mailbox-api/pb"

protoc -I "$ROOT/proto" \
  --go_out="$ROOT/services/mailbox-api/pb" \
  --go-grpc_out="$ROOT/services/mailbox-api/pb" \
  "$ROOT/proto/email.proto" \
  "$ROOT/proto/mailbox_register.proto" \
  "$ROOT/proto/mailbox_service.proto"
