#!/usr/bin/env sh
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
PATH="$(go env GOPATH)/bin:$PATH"

rm -rf "$ROOT/providers/outlook/imap-service/pb"
mkdir -p "$ROOT/providers/outlook/imap-service/pb"

protoc -I "$ROOT/proto" \
  --go_out="$ROOT/providers/outlook/imap-service/pb" \
  --go-grpc_out="$ROOT/providers/outlook/imap-service/pb" \
  "$ROOT/proto/email.proto"

rm -rf "$ROOT/services/mailbox-api/pb"
mkdir -p "$ROOT/services/mailbox-api/pb"

protoc -I "$ROOT/proto" \
  --go_out="$ROOT/services/mailbox-api/pb" \
  --go-grpc_out="$ROOT/services/mailbox-api/pb" \
  "$ROOT/proto/email.proto" \
  "$ROOT/proto/mailbox_register.proto" \
  "$ROOT/proto/mailbox_service.proto"

python3 -m grpc_tools.protoc -I "$ROOT/proto" \
  --python_out="$ROOT/providers/outlook/register-service" \
  --grpc_python_out="$ROOT/providers/outlook/register-service" \
  "$ROOT/proto/mailbox_register.proto"
