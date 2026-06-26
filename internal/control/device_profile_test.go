package control

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/jfelipesjc/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

// fixture mirrors the relevant subset of
// testdata/traces/connect_pair/client_payload.json (same shape the waproto test
// uses), so we can prove DefaultProfile reproduces the captured payload.
type fixture struct {
	PayloadHex string `json:"payloadHex"`
	Creds      struct {
		SignedIdentityKey struct {
			Public string `json:"public"`
		} `json:"signedIdentityKey"`
		SignedPreKey struct {
			KeyID   uint32 `json:"keyId"`
			KeyPair struct {
				Public string `json:"public"`
			} `json:"keyPair"`
			Signature string `json:"signature"`
		} `json:"signedPreKey"`
		RegistrationID uint32 `json:"registrationId"`
	} `json:"creds"`
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	// internal/control -> repo root is ../..
	p := filepath.Join("..", "..", "testdata", "traces", "connect_pair", "client_payload.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var f fixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return f
}

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64 decode %q: %v", s, err)
	}
	return b
}

// fixtureRegInput builds the key-material part of the RegInput from the fixture
// (no fingerprint fields — those come from the profile under test).
func fixtureRegInput(t *testing.T, f fixture) waproto.RegInput {
	t.Helper()
	var ident [32]byte
	copy(ident[:], b64(t, f.Creds.SignedIdentityKey.Public))
	var skVal [32]byte
	copy(skVal[:], b64(t, f.Creds.SignedPreKey.KeyPair.Public))
	var skSig [64]byte
	copy(skSig[:], b64(t, f.Creds.SignedPreKey.Signature))
	return waproto.RegInput{
		RegistrationID:  f.Creds.RegistrationID,
		IdentityPub:     ident,
		SignedPreKeyID:  f.Creds.SignedPreKey.KeyID,
		SignedPreKeyPub: skVal,
		SignedPreKeySig: skSig,
		SyncFull:        false,
	}
}

// TestDefaultProfileReproducesFixture is the critical regression: building the
// RegistrationPayload with DefaultProfile() must equal the captured fixture
// field-by-field (proto.Equal is order-independent; see ADR-001).
func TestDefaultProfileReproducesFixture(t *testing.T) {
	f := loadFixture(t)

	in := DefaultProfile().RegInput(fixtureRegInput(t, f))
	got, err := waproto.RegistrationPayload(in)
	if err != nil {
		t.Fatalf("RegistrationPayload: %v", err)
	}

	raw, err := hex.DecodeString(f.PayloadHex)
	if err != nil {
		t.Fatalf("decode payloadHex: %v", err)
	}
	var want waproto.ClientPayload
	if err := proto.Unmarshal(raw, &want); err != nil {
		t.Fatalf("proto.Unmarshal fixture: %v", err)
	}

	if !proto.Equal(got, &want) {
		t.Errorf("DefaultProfile payload mismatch (field-by-field)\n got: %v\nwant: %v", got, &want)
	}

	// Also assert the UserAgent fingerprint fields equal the historical defaults.
	ua := got.GetUserAgent()
	if ua.GetOsVersion() != "0.1" || ua.GetDevice() != "Desktop" || ua.GetOsBuildNumber() != "0.1" {
		t.Errorf("default UA fingerprint changed: os=%q device=%q build=%q",
			ua.GetOsVersion(), ua.GetDevice(), ua.GetOsBuildNumber())
	}
	if ua.GetLocaleLanguageIso6391() != "en" || ua.GetMcc() != "000" || ua.GetMnc() != "000" {
		t.Errorf("default UA locale changed: lang=%q mcc=%q mnc=%q",
			ua.GetLocaleLanguageIso6391(), ua.GetMcc(), ua.GetMnc())
	}
}

// TestRandomDesktopProfileDeterministic: the same seed yields an identical
// profile, and (with overwhelming probability) different seeds differ.
func TestRandomDesktopProfileDeterministic(t *testing.T) {
	a := RandomDesktopProfile(42)
	b := RandomDesktopProfile(42)
	if a != b {
		t.Fatalf("same seed produced different profiles:\n a=%+v\n b=%+v", a, b)
	}

	// Scan a range of seeds; at least one must differ from seed 42's result,
	// proving the seed actually drives selection.
	diff := false
	for s := int64(0); s < 50; s++ {
		if RandomDesktopProfile(s) != a {
			diff = true
			break
		}
	}
	if !diff {
		t.Fatal("no seed in [0,50) produced a profile different from seed 42")
	}
}

// validCombos is the set of OS->browsers pairings we consider coherent. Used to
// assert RandomDesktopProfile never emits an impossible fingerprint.
var validCombos = map[string]map[string]bool{
	"Windows": {"Chrome": true, "Edge": true, "Firefox": true},
	"Ubuntu":  {"Chrome": true, "Firefox": true},
	"Mac OS":  {"Chrome": true, "Safari": true, "Firefox": true},
}

// TestRandomDesktopProfileValidCombos: across many seeds, every generated
// profile has a browser that actually runs on its OS, a non-empty locale, and a
// non-zero client version.
func TestRandomDesktopProfileValidCombos(t *testing.T) {
	for s := int64(0); s < 500; s++ {
		p := RandomDesktopProfile(s)
		os, br := p.Browser[0], p.Browser[1]
		browsers, ok := validCombos[os]
		if !ok {
			t.Fatalf("seed %d: unknown OS %q", s, os)
		}
		if !browsers[br] {
			t.Fatalf("seed %d: invalid combo %q on %q", s, br, os)
		}
		if p.LocaleLang == "" || p.LocaleCountry == "" || p.MCC == "" || p.MNC == "" {
			t.Fatalf("seed %d: incomplete locale: %+v", s, p)
		}
		if p.ClientVersion == [3]uint32{} {
			t.Fatalf("seed %d: zero client version", s)
		}
	}
}

// TestDifferentProfilesProduceDifferentPayloads: a non-default profile must yield
// a registration payload whose bytes differ from the default one's.
func TestDifferentProfilesProduceDifferentPayloads(t *testing.T) {
	f := loadFixture(t)
	base := fixtureRegInput(t, f)

	defPayload, err := waproto.RegistrationPayload(DefaultProfile().RegInput(base))
	if err != nil {
		t.Fatalf("default payload: %v", err)
	}
	defBytes, _ := proto.Marshal(defPayload)

	// Find a random profile that differs from the default fingerprint.
	var altProfile DeviceProfile
	for s := int64(0); s < 100; s++ {
		p := RandomDesktopProfile(s)
		if p.Browser != DefaultProfile().Browser || p.LocaleCountry != DefaultProfile().LocaleCountry {
			altProfile = p
			break
		}
	}
	if (altProfile == DeviceProfile{}) {
		t.Fatal("could not find a profile differing from default")
	}

	altPayload, err := waproto.RegistrationPayload(altProfile.RegInput(base))
	if err != nil {
		t.Fatalf("alt payload: %v", err)
	}
	altBytes, _ := proto.Marshal(altPayload)

	if bytes.Equal(defBytes, altBytes) {
		t.Fatal("different profile produced identical payload bytes")
	}
	if proto.Equal(defPayload, altPayload) {
		t.Fatal("different profile produced field-equal payload")
	}
}

// TestRandomDesktopProfileFromSource exercises the injectable-source core
// directly, confirming it does not touch the global rand state.
func TestRandomDesktopProfileFromSource(t *testing.T) {
	r1 := rand.New(rand.NewSource(7))
	r2 := rand.New(rand.NewSource(7))
	if randomDesktopProfileFrom(r1) != randomDesktopProfileFrom(r2) {
		t.Fatal("same source seed produced different profiles")
	}
}
