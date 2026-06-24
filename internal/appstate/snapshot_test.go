package appstate

import (
	"testing"

	waproto "github.com/felipeleal/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

// TestEncodeDecodeSnapshotRoundTrip builds a synthetic snapshot with two records
// (a contact and a mute) via EncodeSnapshot, then decodes it with DecodeSnapshot
// and asserts the version, LTHash, mutations and snapshot MAC all validate.
func TestEncodeDecodeSnapshotRoundTrip(t *testing.T) {
	mk := testMutationKeys(t)
	keyIDB64 := testKeyIDB64(t)
	rawKey := mustHex(t, loadVector(t).AppStateSyncKey)

	const name = "critical_unblock_low"
	muts := []MutationToEncode{
		{
			Operation:  waproto.SyncdMutation_SET,
			Index:      []string{"contact", "5511999999999@s.whatsapp.net"},
			APIVersion: 6,
			Action: &waproto.SyncActionValue{
				ContactAction: &waproto.SyncActionValue_ContactAction{
					FullName:  proto.String("Alice Example"),
					FirstName: proto.String("Alice"),
				},
			},
		},
		{
			Operation:  waproto.SyncdMutation_SET,
			Index:      []string{"mute", "5511888888888@s.whatsapp.net"},
			APIVersion: 2,
			Action: &waproto.SyncActionValue{
				MuteAction: &waproto.SyncActionValue_MuteAction{Muted: proto.Bool(true)},
			},
		},
	}

	const version = 7
	snap, err := EncodeSnapshot(name, version, keyIDB64, mk, muts, newFixedReader())
	if err != nil {
		t.Fatalf("EncodeSnapshot: %v", err)
	}
	// Round-trip through bytes to mimic the wire.
	raw, err := proto.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var got waproto.SyncdSnapshot
	if err := proto.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}

	resolve := func(id string) ([]byte, bool) {
		if id == keyIDB64 {
			return rawKey, true
		}
		return nil, false
	}
	res, err := DecodeSnapshot(name, &got, resolve)
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}
	if res.State.Version != version {
		t.Errorf("version = %d, want %d", res.State.Version, version)
	}
	if len(res.Mutations) != 2 {
		t.Fatalf("mutations = %d, want 2", len(res.Mutations))
	}
	ca := res.Mutations[0].Action.GetContactAction()
	if ca == nil || ca.GetFullName() != "Alice Example" {
		t.Fatalf("contact mutation wrong: %v", res.Mutations[0].Action)
	}

	// A patch applied on top of the snapshot must chain from the snapshot LTHash.
	patchBytes, _, err := EncodePatch(name, res.State.Version, keyIDB64, mk, res.State,
		[]MutationToEncode{{
			Operation:  waproto.SyncdMutation_SET,
			Index:      []string{"mute", "5511777777777@s.whatsapp.net"},
			APIVersion: 2,
			Action:     &waproto.SyncActionValue{MuteAction: &waproto.SyncActionValue_MuteAction{Muted: proto.Bool(true)}},
		}}, newFixedReader())
	if err != nil {
		t.Fatalf("EncodePatch on snapshot: %v", err)
	}
	var patch waproto.SyncdPatch
	if err := proto.Unmarshal(patchBytes, &patch); err != nil {
		t.Fatalf("unmarshal patch: %v", err)
	}
	if patch.GetVersion().GetVersion() != version+1 {
		t.Errorf("patch version = %d, want %d", patch.GetVersion().GetVersion(), version+1)
	}
	pres, err := DecodePatch(name, &patch, res.State, resolve)
	if err != nil {
		t.Fatalf("DecodePatch on snapshot state: %v", err)
	}
	if pres.State.Version != version+1 {
		t.Errorf("post-patch version = %d, want %d", pres.State.Version, version+1)
	}
}

// TestDecodeSnapshotTamper ensures a corrupted record fails the MAC check.
func TestDecodeSnapshotTamper(t *testing.T) {
	mk := testMutationKeys(t)
	keyIDB64 := testKeyIDB64(t)
	rawKey := mustHex(t, loadVector(t).AppStateSyncKey)
	snap, err := EncodeSnapshot("regular", 1, keyIDB64, mk, []MutationToEncode{{
		Operation:  waproto.SyncdMutation_SET,
		Index:      []string{"contact", "x@s.whatsapp.net"},
		APIVersion: 6,
		Action:     &waproto.SyncActionValue{ContactAction: &waproto.SyncActionValue_ContactAction{FullName: proto.String("X")}},
	}}, newFixedReader())
	if err != nil {
		t.Fatalf("EncodeSnapshot: %v", err)
	}
	snap.GetRecords()[0].GetValue().Blob[0] ^= 0xff // corrupt
	resolve := func(id string) ([]byte, bool) {
		if id == keyIDB64 {
			return rawKey, true
		}
		return nil, false
	}
	if _, err := DecodeSnapshot("regular", snap, resolve); err == nil {
		t.Fatal("expected MAC error on tampered snapshot")
	}
}
