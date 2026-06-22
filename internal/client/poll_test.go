package client

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

const (
	testPollMsgID   = "3EB0XXXXXXXXXXXXXXXX"
	testPollCreator = "5511999999999@s.whatsapp.net"
	testPollVoter   = "5511888888888@s.whatsapp.net"
)

func newMessageSecret(t *testing.T) []byte {
	t.Helper()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return secret
}

// TestPollVoteRoundTrip encrypts a vote with EncryptPollVote and confirms
// DecryptPollVote recovers the exact option hashes.
func TestPollVoteRoundTrip(t *testing.T) {
	secret := newMessageSecret(t)
	options := []string{"Pizza", "Sushi", "Tacos"}

	// Voter selected "Pizza" and "Tacos".
	selected := [][]byte{
		HashPollOption("Pizza"),
		HashPollOption("Tacos"),
	}

	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		t.Fatalf("rand iv: %v", err)
	}

	encPayload, err := EncryptPollVote(selected, iv, testPollMsgID, testPollCreator, testPollVoter, secret)
	if err != nil {
		t.Fatalf("EncryptPollVote: %v", err)
	}
	if len(encPayload) <= gcmTagLen {
		t.Fatalf("encPayload too short: %d", len(encPayload))
	}

	got, err := DecryptPollVote(encPayload, iv, testPollMsgID, testPollCreator, testPollVoter, secret)
	if err != nil {
		t.Fatalf("DecryptPollVote: %v", err)
	}
	if len(got) != len(selected) {
		t.Fatalf("got %d hashes, want %d", len(got), len(selected))
	}
	for i := range selected {
		if !bytes.Equal(got[i], selected[i]) {
			t.Errorf("hash[%d] = %x, want %x", i, got[i], selected[i])
		}
	}

	// MatchPollOptions should map the hashes back to names, in option order.
	names := MatchPollOptions(got, options)
	want := []string{"Pizza", "Tacos"}
	if len(names) != len(want) {
		t.Fatalf("matched %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("name[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

// TestPollVoteEmpty covers a vote that selects no options (un-voting).
func TestPollVoteEmpty(t *testing.T) {
	secret := newMessageSecret(t)
	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		t.Fatalf("rand iv: %v", err)
	}

	encPayload, err := EncryptPollVote(nil, iv, testPollMsgID, testPollCreator, testPollVoter, secret)
	if err != nil {
		t.Fatalf("EncryptPollVote: %v", err)
	}
	got, err := DecryptPollVote(encPayload, iv, testPollMsgID, testPollCreator, testPollVoter, secret)
	if err != nil {
		t.Fatalf("DecryptPollVote: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d hashes, want 0", len(got))
	}
	if names := MatchPollOptions(got, []string{"A", "B"}); names != nil {
		t.Fatalf("MatchPollOptions = %v, want nil", names)
	}
}

// TestPollVoteWrongContextFails confirms the AAD / key derivation binds the vote
// to the (pollMsgId, creator, voter) tuple: changing any of them breaks GCM.
func TestPollVoteWrongContextFails(t *testing.T) {
	secret := newMessageSecret(t)
	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		t.Fatalf("rand iv: %v", err)
	}
	selected := [][]byte{HashPollOption("Yes")}
	encPayload, err := EncryptPollVote(selected, iv, testPollMsgID, testPollCreator, testPollVoter, secret)
	if err != nil {
		t.Fatalf("EncryptPollVote: %v", err)
	}

	cases := []struct {
		name                  string
		msgID, creator, voter string
		secret                []byte
	}{
		{"wrong voter", testPollMsgID, testPollCreator, "5511777777777@s.whatsapp.net", secret},
		{"wrong creator", testPollMsgID, "5511000000000@s.whatsapp.net", testPollVoter, secret},
		{"wrong msgID", "DIFFERENTID", testPollCreator, testPollVoter, secret},
		{"wrong secret", testPollMsgID, testPollCreator, testPollVoter, newMessageSecret(t)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := DecryptPollVote(encPayload, iv, c.msgID, c.creator, c.voter, c.secret); err == nil {
				t.Fatal("expected decryption to fail, got nil error")
			}
		})
	}
}

// TestPollVoteKeyDerivationKAT pins the exact HMAC chain Baileys uses, so any
// accidental change to the derivation (HMAC arg order, salt, info bytes) is
// caught even without a real Baileys vector. The expected key is recomputed here
// independently from the helper, against fixed inputs.
func TestPollVoteKeyDerivationKAT(t *testing.T) {
	// Fixed, deterministic secret (0x00..0x1f).
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i)
	}
	msgID := "POLLID123"
	creator := "1@s.whatsapp.net"
	voter := "2@s.whatsapp.net"

	// Independent reference computation of the Baileys derivation:
	//   key0   = HMAC-SHA256(key=zeros(32), data=secret)
	//   decKey = HMAC-SHA256(key=key0, data=msgID||creator||voter||"Poll Vote"||0x01)
	var zeros [32]byte
	h0 := hmac.New(sha256.New, zeros[:])
	h0.Write(secret)
	key0 := h0.Sum(nil)

	var sign bytes.Buffer
	sign.WriteString(msgID)
	sign.WriteString(creator)
	sign.WriteString(voter)
	sign.WriteString("Poll Vote")
	sign.WriteByte(0x01)
	h1 := hmac.New(sha256.New, key0)
	h1.Write(sign.Bytes())
	wantKey := h1.Sum(nil)

	gotKey := derivePollVoteKey(secret, msgID, creator, voter)
	if !bytes.Equal(gotKey, wantKey) {
		t.Fatalf("derivePollVoteKey = %s, want %s", hex.EncodeToString(gotKey), hex.EncodeToString(wantKey))
	}

	// Also assert the AAD layout: pollMsgId || 0x00 || voterJid.
	wantAAD := append(append([]byte(msgID), 0x00), []byte(voter)...)
	if gotAAD := pollVoteAAD(msgID, voter); !bytes.Equal(gotAAD, wantAAD) {
		t.Fatalf("pollVoteAAD = %x, want %x", gotAAD, wantAAD)
	}
}

// TestPollVoteMessageCodec checks the hand-rolled protobuf codec for
// PollVoteMessage{ repeated bytes selectedOptions = 1 }.
func TestPollVoteMessageCodec(t *testing.T) {
	opts := [][]byte{
		HashPollOption("alpha"),
		HashPollOption("beta"),
	}
	enc := encodePollVoteMessage(opts)
	// Each entry: tag 0x0A, length 0x20 (32), then 32 bytes.
	if enc[0] != 0x0A || enc[1] != 0x20 {
		t.Fatalf("unexpected encoding prefix: %x", enc[:2])
	}
	dec, err := decodePollVoteMessage(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(dec) != 2 || !bytes.Equal(dec[0], opts[0]) || !bytes.Equal(dec[1], opts[1]) {
		t.Fatalf("round trip mismatch: %x", dec)
	}
}
