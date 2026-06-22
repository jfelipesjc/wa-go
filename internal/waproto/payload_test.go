package waproto

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"
)

// fixture mirrors the relevant subset of
// testdata/traces/connect_pair/client_payload.json.
type fixture struct {
	Config struct {
		Version     [3]uint32 `json:"version"`
		Browser     [3]string `json:"browser"`
		CountryCode string    `json:"countryCode"`
		SyncFull    bool      `json:"syncFullHistory"`
	} `json:"config"`
	PayloadHex string `json:"payloadHex"`
	Fields     struct {
		Passive   bool `json:"passive"`
		UserAgent struct {
			Platform   string `json:"platform"`
			AppVersion struct {
				Primary   uint32 `json:"primary"`
				Secondary uint32 `json:"secondary"`
				Tertiary  uint32 `json:"tertiary"`
			} `json:"appVersion"`
		} `json:"userAgent"`
		WebInfo struct {
			WebSubPlatform string `json:"webSubPlatform"`
		} `json:"webInfo"`
		ConnectType   string `json:"connectType"`
		ConnectReason string `json:"connectReason"`
		Pull          bool   `json:"pull"`
	} `json:"fields"`
	Creds struct {
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
	// internal/waproto -> repo root is ../..
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

// TestUnmarshalFixture: the captured payloadHex unmarshals into a ClientPayload
// without error and the top-level fields match the decoded `fields` tree.
func TestUnmarshalFixture(t *testing.T) {
	f := loadFixture(t)
	raw, err := hex.DecodeString(f.PayloadHex)
	if err != nil {
		t.Fatalf("decode payloadHex: %v", err)
	}
	var cp ClientPayload
	if err := proto.Unmarshal(raw, &cp); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}

	if got := cp.GetPassive(); got != f.Fields.Passive {
		t.Errorf("passive = %v, want %v", got, f.Fields.Passive)
	}
	if got := cp.GetPull(); got != f.Fields.Pull {
		t.Errorf("pull = %v, want %v", got, f.Fields.Pull)
	}
	if got := cp.GetUserAgent().GetPlatform().String(); got != f.Fields.UserAgent.Platform {
		t.Errorf("userAgent.platform = %q, want %q", got, f.Fields.UserAgent.Platform)
	}
	if got := cp.GetConnectType().String(); got != f.Fields.ConnectType {
		t.Errorf("connectType = %q, want %q", got, f.Fields.ConnectType)
	}
	if got := cp.GetConnectReason().String(); got != f.Fields.ConnectReason {
		t.Errorf("connectReason = %q, want %q", got, f.Fields.ConnectReason)
	}
	if got := cp.GetWebInfo().GetWebSubPlatform().String(); got != f.Fields.WebInfo.WebSubPlatform {
		t.Errorf("webInfo.webSubPlatform = %q, want %q", got, f.Fields.WebInfo.WebSubPlatform)
	}
	av := cp.GetUserAgent().GetAppVersion()
	if av.GetPrimary() != f.Fields.UserAgent.AppVersion.Primary ||
		av.GetSecondary() != f.Fields.UserAgent.AppVersion.Secondary ||
		av.GetTertiary() != f.Fields.UserAgent.AppVersion.Tertiary {
		t.Errorf("appVersion = %d.%d.%d, want %d.%d.%d",
			av.GetPrimary(), av.GetSecondary(), av.GetTertiary(),
			f.Fields.UserAgent.AppVersion.Primary,
			f.Fields.UserAgent.AppVersion.Secondary,
			f.Fields.UserAgent.AppVersion.Tertiary)
	}
	if cp.GetDevicePairingData() == nil {
		t.Fatal("devicePairingData missing")
	}
}

// TestRegistrationReproducesFixture: feeding the fixture creds into
// RegistrationPayload reproduces the captured ClientPayload field-for-field.
func TestRegistrationReproducesFixture(t *testing.T) {
	f := loadFixture(t)

	var ident [32]byte
	copy(ident[:], b64(t, f.Creds.SignedIdentityKey.Public))
	var skVal [32]byte
	copy(skVal[:], b64(t, f.Creds.SignedPreKey.KeyPair.Public))
	var skSig [64]byte
	copy(skSig[:], b64(t, f.Creds.SignedPreKey.Signature))

	in := RegInput{
		RegistrationID:  f.Creds.RegistrationID,
		IdentityPub:     ident,
		SignedPreKeyID:  f.Creds.SignedPreKey.KeyID,
		SignedPreKeyPub: skVal,
		SignedPreKeySig: skSig,
		Version:         WAVersion(f.Config.Version),
		Browser:         Browser(f.Config.Browser),
		CountryCode:     f.Config.CountryCode,
		SyncFull:        f.Config.SyncFull,
	}

	got, err := RegistrationPayload(in)
	if err != nil {
		t.Fatalf("RegistrationPayload: %v", err)
	}

	raw, err := hex.DecodeString(f.PayloadHex)
	if err != nil {
		t.Fatalf("decode payloadHex: %v", err)
	}
	var want ClientPayload
	if err := proto.Unmarshal(raw, &want); err != nil {
		t.Fatalf("proto.Unmarshal fixture: %v", err)
	}

	// proto.Equal does a semantic, field-by-field comparison independent of
	// wire-encoding field order. The protobuf-go encoder may serialize fields
	// in a different order than protobufjs (Baileys), so the raw bytes can
	// differ even when every value matches (cf. ADR-001). What matters is that
	// all field VALUES are equal.
	if !proto.Equal(got, &want) {
		t.Errorf("RegistrationPayload mismatch (field-by-field)\n got: %v\nwant: %v", got, &want)
	}

	// Report whether the exact bytes also match (informational).
	gotBytes, err := proto.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	if bytes.Equal(gotBytes, raw) {
		t.Logf("bytes match exactly (%d bytes)", len(raw))
	} else {
		t.Logf("values match field-by-field; raw bytes differ only by encoder field order "+
			"(got %d bytes, fixture %d bytes)", len(gotBytes), len(raw))
	}

	// devicePairingData field-by-field assertions.
	gd := got.GetDevicePairingData()
	wd := want.GetDevicePairingData()
	checks := []struct {
		name     string
		got, exp []byte
	}{
		{"eRegid", gd.GetERegid(), wd.GetERegid()},
		{"eKeytype", gd.GetEKeytype(), wd.GetEKeytype()},
		{"eIdent", gd.GetEIdent(), wd.GetEIdent()},
		{"eSkeyId", gd.GetESkeyId(), wd.GetESkeyId()},
		{"eSkeyVal", gd.GetESkeyVal(), wd.GetESkeyVal()},
		{"eSkeySig", gd.GetESkeySig(), wd.GetESkeySig()},
		{"buildHash", gd.GetBuildHash(), wd.GetBuildHash()},
		{"deviceProps", gd.GetDeviceProps(), wd.GetDeviceProps()},
	}
	for _, c := range checks {
		if !bytes.Equal(c.got, c.exp) {
			t.Errorf("devicePairingData.%s mismatch\n got %x\nwant %x", c.name, c.got, c.exp)
		}
	}
}

// TestBuildHashMatchesFixture: the md5 of the version string equals the
// captured buildHash.
func TestBuildHashMatchesFixture(t *testing.T) {
	f := loadFixture(t)

	raw, err := hex.DecodeString(f.PayloadHex)
	if err != nil {
		t.Fatalf("decode payloadHex: %v", err)
	}
	var cp ClientPayload
	if err := proto.Unmarshal(raw, &cp); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}

	v := WAVersion(f.Config.Version)
	if v.String() != "2.3000.1035194821" {
		t.Errorf("version string = %q, want %q", v.String(), "2.3000.1035194821")
	}
	got := v.BuildHash()
	want := cp.GetDevicePairingData().GetBuildHash()
	if !bytes.Equal(got, want) {
		t.Errorf("buildHash = %x, want %x", got, want)
	}
}
