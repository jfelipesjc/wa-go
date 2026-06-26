package appstate

import (
	"bytes"
	"testing"

	waproto "github.com/jfelipesjc/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

// fixedReader yields a deterministic, repeating byte stream so EncodePatch's IV
// (and hence the whole patch) is reproducible across runs.
type fixedReader struct {
	b   []byte
	pos int
}

func (r *fixedReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.b[r.pos%len(r.b)]
		r.pos++
	}
	return len(p), nil
}

func newFixedReader() *fixedReader {
	return &fixedReader{b: []byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
	}}
}

func testMutationKeys(t *testing.T) MutationKeys {
	t.Helper()
	v := loadVector(t)
	return DeriveMutationKeys(mustHex(t, v.AppStateSyncKey))
}

func testKeyIDB64(t *testing.T) string {
	t.Helper()
	return loadVector(t).KeyIDBase64
}

// TestEncodeDecodeRoundTrip encodes an archive and a mute mutation, then decodes
// the produced patch and checks the operations, indices, action values, MACs and
// final LTHash all match. This exercises the encode/decode contract end to end.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	mk := testMutationKeys(t)
	keyIDB64 := testKeyIDB64(t)
	v := loadVector(t)
	rawKey := mustHex(t, v.AppStateSyncKey)

	muts := []MutationToEncode{
		{
			Operation:  waproto.SyncdMutation_SET,
			Index:      []string{"archive", "5511999999999@s.whatsapp.net"},
			APIVersion: 3,
			Action: &waproto.SyncActionValue{
				ArchiveChatAction: &waproto.SyncActionValue_ArchiveChatAction{
					Archived: proto.Bool(true),
				},
			},
		},
		{
			Operation:  waproto.SyncdMutation_SET,
			Index:      []string{"mute", "5511888888888@s.whatsapp.net"},
			APIVersion: 2,
			Action: &waproto.SyncActionValue{
				MuteAction: &waproto.SyncActionValue_MuteAction{
					Muted:            proto.Bool(true),
					MuteEndTimestamp: proto.Int64(1893456000000),
				},
			},
		},
	}

	const name = "regular_low"
	patchBytes, encState, err := EncodePatch(name, 0, keyIDB64, mk, NewHashState(), muts, newFixedReader())
	if err != nil {
		t.Fatalf("EncodePatch: %v", err)
	}

	var patch waproto.SyncdPatch
	if err := proto.Unmarshal(patchBytes, &patch); err != nil {
		t.Fatalf("unmarshal patch: %v", err)
	}
	if patch.GetVersion().GetVersion() != 1 {
		t.Errorf("patch version = %d, want 1", patch.GetVersion().GetVersion())
	}

	resolve := func(id string) ([]byte, bool) {
		if id == keyIDB64 {
			return rawKey, true
		}
		return nil, false
	}

	res, err := DecodePatch(name, &patch, NewHashState(), resolve)
	if err != nil {
		t.Fatalf("DecodePatch round-trip: %v", err)
	}

	if res.State.Version != encState.Version {
		t.Errorf("decoded version = %d, want %d", res.State.Version, encState.Version)
	}
	if !bytes.Equal(res.State.Hash, encState.Hash) {
		t.Errorf("decoded LTHash != encoded LTHash\n got %x\nwant %x", res.State.Hash, encState.Hash)
	}
	if len(res.Mutations) != len(muts) {
		t.Fatalf("decoded %d mutations, want %d", len(res.Mutations), len(muts))
	}

	// archive
	a := res.Mutations[0]
	if a.Operation != waproto.SyncdMutation_SET {
		t.Errorf("mutation 0 op = %v, want SET", a.Operation)
	}
	if got := a.Index; len(got) != 2 || got[0] != "archive" {
		t.Errorf("mutation 0 index = %v", got)
	}
	if ac := a.Action.GetArchiveChatAction(); ac == nil || !ac.GetArchived() {
		t.Errorf("mutation 0 archiveChatAction not archived: %v", a.Action)
	}

	// mute
	m := res.Mutations[1]
	if got := m.Index; len(got) != 2 || got[0] != "mute" {
		t.Errorf("mutation 1 index = %v", got)
	}
	ma := m.Action.GetMuteAction()
	if ma == nil || !ma.GetMuted() || ma.GetMuteEndTimestamp() != 1893456000000 {
		t.Errorf("mutation 1 muteAction mismatch: %v", ma)
	}
}

// TestEncodePatchDeterministic verifies that with a fixed IV reader the encoded
// bytes are reproducible.
func TestEncodePatchDeterministic(t *testing.T) {
	mk := testMutationKeys(t)
	keyIDB64 := testKeyIDB64(t)
	muts := []MutationToEncode{{
		Operation:  waproto.SyncdMutation_SET,
		Index:      []string{"pin_v1", "5511777777777@s.whatsapp.net"},
		APIVersion: 5,
		Action: &waproto.SyncActionValue{
			PinAction: &waproto.SyncActionValue_PinAction{Pinned: proto.Bool(true)},
		},
	}}

	p1, s1, err := EncodePatch("regular_low", 7, keyIDB64, mk, NewHashState(), muts, newFixedReader())
	if err != nil {
		t.Fatalf("EncodePatch #1: %v", err)
	}
	p2, s2, err := EncodePatch("regular_low", 7, keyIDB64, mk, NewHashState(), muts, newFixedReader())
	if err != nil {
		t.Fatalf("EncodePatch #2: %v", err)
	}
	if !bytes.Equal(p1, p2) {
		t.Error("EncodePatch not deterministic with fixed IV reader")
	}
	if s1.Version != 8 || s2.Version != 8 {
		t.Errorf("version after encode = %d/%d, want 8", s1.Version, s2.Version)
	}
	if !bytes.Equal(s1.Hash, s2.Hash) {
		t.Error("encoded states differ")
	}
}

// TestEncodeValueMacScheme cross-checks that the valueMac EncodePatch writes is
// exactly the one generateContentMac (the decoder's scheme) recomputes from the
// encrypted blob, with the op byte SET=0x00 / REMOVE=0x01.
func TestEncodeValueMacScheme(t *testing.T) {
	mk := testMutationKeys(t)
	keyIDB64 := testKeyIDB64(t)
	encKeyID, err := decodeB64(keyIDB64)
	if err != nil {
		t.Fatal(err)
	}

	for _, op := range []waproto.SyncdMutation_SyncdOperation{
		waproto.SyncdMutation_SET, waproto.SyncdMutation_REMOVE,
	} {
		muts := []MutationToEncode{{
			Operation:  op,
			Index:      []string{"contact", "5511666666666@s.whatsapp.net"},
			APIVersion: 2,
			Action: &waproto.SyncActionValue{
				ContactAction: &waproto.SyncActionValue_ContactAction{
					FullName: proto.String("X"),
				},
			},
		}}
		patchBytes, _, err := EncodePatch("critical_unblock_low", 0, keyIDB64, mk, NewHashState(), muts, newFixedReader())
		if err != nil {
			t.Fatalf("EncodePatch op=%v: %v", op, err)
		}
		var patch waproto.SyncdPatch
		if err := proto.Unmarshal(patchBytes, &patch); err != nil {
			t.Fatal(err)
		}
		blob := patch.Mutations[0].Record.Value.Blob
		encValue := blob[:len(blob)-32]
		gotMac := blob[len(blob)-32:]
		want := generateContentMac(op, encValue, encKeyID, mk.ValueMacKey)
		if !bytes.Equal(gotMac, want) {
			t.Errorf("op=%v valueMac mismatch: got %x want %x", op, gotMac, want)
		}
	}
}
