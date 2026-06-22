// Package waproto contains the subset of the WhatsApp Web protobuf schema
// needed for pairing/auth: ClientPayload (and its dependencies) plus the ADV*
// device-identity messages used by the pair-success flow.
//
// The .proto is a minimal extract of the upstream Baileys schema at
// harness/node_modules/@whiskeysockets/baileys/WAProto/WAProto.proto, keeping
// the original field numbers so captured fixtures round-trip.
//
// To regenerate waproto.pb.go after editing waproto.proto, ensure protoc and
// protoc-gen-go are on PATH and run, from the module root:
//
//	protoc --go_out=. --go_opt=paths=source_relative internal/waproto/waproto.proto
package waproto

//go:generate protoc --go_out=. --go_opt=paths=source_relative waproto.proto
