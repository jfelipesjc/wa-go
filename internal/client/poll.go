package client

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"

	waproto "github.com/felipeleal/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

// Poll vote decryption.
//
// When a poll is created, the creator embeds a 32-byte messageSecret in the
// pollCreation message's MessageContextInfo.messageSecret. Each voter encrypts
// their vote (a PollVoteMessage carrying SHA-256 hashes of the option names they
// selected) under a key derived from that shared secret, using AES-256-GCM.
//
// The key derivation mirrors Baileys' decryptPollVote
// (Utils/process-message.js). It is NOT a standard single-call HKDF: it is two
// chained HMAC-SHA256 rounds that happen to coincide with HKDF-Extract followed
// by one HKDF-Expand block:
//
//	key0   = HMAC-SHA256(key = zeros(32), data = messageSecret)        // HKDF-Extract, salt = 0^32, IKM = secret
//	sign   = pollMsgId || pollCreatorJid || voterJid || "Poll Vote" || 0x01
//	decKey = HMAC-SHA256(key = key0, data = sign)                      // HKDF-Expand, info = sign[:-1], counter byte 0x01
//
// The additional authenticated data (AAD) for the GCM operation is:
//
//	aad = pollMsgId || 0x00 || voterJid
//
// The GCM auth tag is appended to the ciphertext (last 16 bytes), as produced by
// Node's crypto and by Go's cipher.AEAD.Seal.
//
// Baileys reference (lib/Utils/process-message.js, decryptPollVote):
//
//	const sign = Buffer.concat([
//	    toBinary(pollMsgId), toBinary(pollCreatorJid), toBinary(voterJid),
//	    toBinary('Poll Vote'), new Uint8Array([1]) ]);
//	const key0   = hmacSign(pollEncKey, new Uint8Array(32), 'sha256'); // HMAC(key=zeros, data=pollEncKey)
//	const decKey = hmacSign(sign, key0, 'sha256');                     // HMAC(key=key0, data=sign)
//	const aad    = toBinary(`${pollMsgId} (0x00) ${voterJid}`);
//	aesDecryptGCM(encPayload, decKey, encIv, aad);
//
// Note: Baileys' hmacSign(buffer, key) computes HMAC(key, buffer) — the first
// argument is the message and the second is the key.
//
// Byte-for-byte validation against a real Baileys-produced vote is left for a
// live test; the offline coverage here is a round-trip (EncryptPollVote ->
// DecryptPollVote) plus a fixed KAT of the key-derivation HMAC chain.

const gcmTagLen = 16

// derivePollVoteKey reproduces the two-round HMAC-SHA256 derivation Baileys uses
// for poll votes. It returns the 32-byte AES-256-GCM key.
func derivePollVoteKey(messageSecret []byte, pollMsgID, pollCreatorJID, voterJID string) []byte {
	// HKDF-Extract: PRK = HMAC(salt=zeros(32), IKM=messageSecret).
	var zeroSalt [32]byte
	h := hmac.New(sha256.New, zeroSalt[:])
	h.Write(messageSecret)
	key0 := h.Sum(nil)

	// HKDF-Expand (single block): OKM = HMAC(PRK, info || 0x01).
	sign := pollVoteSign(pollMsgID, pollCreatorJID, voterJID)
	h2 := hmac.New(sha256.New, key0)
	h2.Write(sign)
	return h2.Sum(nil)
}

// pollVoteSign builds the info buffer fed into the HKDF-Expand HMAC:
// pollMsgId || pollCreatorJid || voterJid || "Poll Vote" || 0x01.
func pollVoteSign(pollMsgID, pollCreatorJID, voterJID string) []byte {
	var b bytes.Buffer
	b.WriteString(pollMsgID)
	b.WriteString(pollCreatorJID)
	b.WriteString(voterJID)
	b.WriteString("Poll Vote")
	b.WriteByte(0x01)
	return b.Bytes()
}

// pollVoteAAD builds the GCM additional authenticated data:
// pollMsgId || 0x00 || voterJid.
func pollVoteAAD(pollMsgID, voterJID string) []byte {
	var b bytes.Buffer
	b.WriteString(pollMsgID)
	b.WriteByte(0x00)
	b.WriteString(voterJID)
	return b.Bytes()
}

// DecryptPollVote decrypts an encrypted poll vote (PollUpdateMessage.vote) and
// returns the list of SHA-256 option hashes the voter selected.
//
// encPayload is the GCM ciphertext with the 16-byte auth tag appended; encIv is
// the 12-byte GCM nonce. messageSecret is the 32-byte secret from the
// pollCreation MessageContextInfo. pollMsgID is the poll-creation message ID,
// pollCreatorJID the poll author's JID, voterJID the JID of the participant who
// cast this vote.
func DecryptPollVote(encPayload, encIv []byte, pollMsgID, pollCreatorJID, voterJID string, messageSecret []byte) (selectedOptionHashes [][]byte, err error) {
	if len(messageSecret) != 32 {
		return nil, fmt.Errorf("poll: messageSecret must be 32 bytes, got %d", len(messageSecret))
	}
	if len(encPayload) < gcmTagLen {
		return nil, errors.New("poll: encPayload shorter than GCM tag")
	}

	decKey := derivePollVoteKey(messageSecret, pollMsgID, pollCreatorJID, voterJID)
	block, err := aes.NewCipher(decKey)
	if err != nil {
		return nil, fmt.Errorf("poll: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCMWithNonceSize(block, len(encIv))
	if err != nil {
		return nil, fmt.Errorf("poll: new gcm: %w", err)
	}

	aad := pollVoteAAD(pollMsgID, voterJID)
	plaintext, err := gcm.Open(nil, encIv, encPayload, aad)
	if err != nil {
		return nil, fmt.Errorf("poll: gcm open: %w", err)
	}

	return decodePollVoteMessage(plaintext)
}

// EncryptPollVote is the inverse of DecryptPollVote: it encrypts a set of
// selected option hashes into an (encPayload, encIv) pair using the same key
// derivation. It is primarily used for round-trip testing, but is also useful if
// the library ever needs to cast votes.
func EncryptPollVote(selectedOptionHashes [][]byte, encIv []byte, pollMsgID, pollCreatorJID, voterJID string, messageSecret []byte) (encPayload []byte, err error) {
	if len(messageSecret) != 32 {
		return nil, fmt.Errorf("poll: messageSecret must be 32 bytes, got %d", len(messageSecret))
	}

	decKey := derivePollVoteKey(messageSecret, pollMsgID, pollCreatorJID, voterJID)
	block, err := aes.NewCipher(decKey)
	if err != nil {
		return nil, fmt.Errorf("poll: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCMWithNonceSize(block, len(encIv))
	if err != nil {
		return nil, fmt.Errorf("poll: new gcm: %w", err)
	}

	plaintext := encodePollVoteMessage(selectedOptionHashes)
	aad := pollVoteAAD(pollMsgID, voterJID)
	// Go appends the tag to the ciphertext, matching Baileys' aesEncryptGCM.
	return gcm.Seal(nil, encIv, plaintext, aad), nil
}

// MatchPollOptions maps decrypted selected option hashes back to the original
// option names from the pollCreation message. It computes SHA-256 of each option
// name and returns the names whose hash appears in selectedHashes, preserving
// the order of options.
func MatchPollOptions(selectedHashes [][]byte, options []string) []string {
	if len(selectedHashes) == 0 {
		return nil
	}
	want := make(map[string]struct{}, len(selectedHashes))
	for _, h := range selectedHashes {
		want[string(h)] = struct{}{}
	}
	var out []string
	for _, opt := range options {
		sum := sha256.Sum256([]byte(opt))
		if _, ok := want[string(sum[:])]; ok {
			out = append(out, opt)
		}
	}
	return out
}

// HashPollOption returns the SHA-256 hash of a poll option name, the value that
// appears (encrypted) inside a poll vote.
func HashPollOption(option string) []byte {
	sum := sha256.Sum256([]byte(option))
	return sum[:]
}

// --- PollVoteMessage wire codec (waproto-backed) ---
//
// PollVoteMessage is `message { repeated bytes selectedOptions = 1; }`. It only
// ever appears as decrypted poll-vote plaintext, so we (un)marshal it via the
// generated waproto.PollVoteMessage rather than a hand-rolled codec.

func encodePollVoteMessage(selectedOptions [][]byte) []byte {
	out, err := proto.Marshal(&waproto.PollVoteMessage{SelectedOptions: selectedOptions})
	if err != nil {
		// PollVoteMessage has only a repeated bytes field, so marshaling cannot
		// fail in practice; return empty rather than panicking.
		return nil
	}
	return out
}

func decodePollVoteMessage(data []byte) ([][]byte, error) {
	var msg waproto.PollVoteMessage
	if err := proto.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("poll: decode PollVoteMessage: %w", err)
	}
	return msg.GetSelectedOptions(), nil
}
