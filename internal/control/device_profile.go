// Package control is the wa-go anti-ban "Control Layer". It groups three
// concerns that make an automated client look and behave like a real human's
// linked device:
//
//   - DeviceProfile (this file): the per-instance fingerprint (browser tuple,
//     OS version, device name, client version, locale) that flows into the
//     ClientPayload UserAgent/DeviceProps. Varying it per instance avoids every
//     instance presenting the same hardcoded Baileys fingerprint.
//   - HumanPacer (pacer.go): human-like send cadence + rate limiting.
//   - Frame hooks (wired in internal/client): raw node inspection/manipulation.
package control

import (
	"math/rand"

	"github.com/felipeleal/wa-go/internal/waproto"
)

// DeviceProfile is the per-instance device fingerprint. Its fields populate the
// UserAgent and DeviceProps submessages of the registration/login ClientPayload.
// Two clients with different DeviceProfiles present different fingerprints to
// WhatsApp, which is the whole point of the Control Layer's (A) piece.
type DeviceProfile struct {
	// Browser is Baileys' browser tuple {os, browser, osVersion}. The first
	// element becomes DeviceProps.os; the second selects DeviceProps.platformType
	// (CHROME/FIREFOX/SAFARI/...). The OS and browser MUST be a plausible pair
	// (e.g. Safari only on macOS) — RandomDesktopProfile guarantees this.
	Browser [3]string

	// OSVersion is UserAgent.osVersion (e.g. "10.15.7" for macOS).
	OSVersion string
	// Device is UserAgent.device (e.g. "Desktop").
	Device string
	// OSBuildNumber is UserAgent.osBuildNumber.
	OSBuildNumber string

	// ClientVersion is the WhatsApp Web client version triple sent in the
	// UserAgent.appVersion and md5-hashed into devicePairingData.buildHash.
	ClientVersion [3]uint32

	// Platform is the DeviceProps platform enum. It is derived from Browser[1] by
	// the payload builder, so it is informational here; kept for callers that want
	// to inspect/override the resolved platform.
	Platform waproto.DeviceProps_PlatformType

	// Locale + carrier fields for the UserAgent.
	LocaleLang    string // localeLanguageIso6391, e.g. "en"
	LocaleCountry string // localeCountryIso31661Alpha2, e.g. "US"
	MCC           string // mobile country code, e.g. "000"
	MNC           string // mobile network code, e.g. "000"
}

// DefaultProfile returns the historical, hardcoded fingerprint — the exact
// values the original client used (Browsers.ubuntu('Chrome'), version
// 2.3000.1035194821, locale US, osVersion/build "0.1", device "Desktop"). Using
// it MUST reproduce testdata/traces/connect_pair/client_payload.json byte-for-byte
// (modulo protobuf field ordering); device_profile_test.go enforces this.
func DefaultProfile() DeviceProfile {
	return DeviceProfile{
		Browser:       [3]string{"Ubuntu", "Chrome", "22.04.4"},
		OSVersion:     "0.1",
		Device:        "Desktop",
		OSBuildNumber: "0.1",
		ClientVersion: [3]uint32{2, 3000, 1035194821},
		Platform:      waproto.DeviceProps_CHROME,
		LocaleLang:    "en",
		LocaleCountry: "US",
		MCC:           "000",
		MNC:           "000",
	}
}

// desktopCombo is one plausible {OS, browser} fingerprint. Browsers are only
// paired with operating systems they actually run on, so RandomDesktopProfile
// never emits an impossible combination (e.g. Safari on Windows).
type desktopCombo struct {
	os        string // Browser[0] / DeviceProps.os
	browser   string // Browser[1] -> platformType
	osVersion string // Browser[2]
	uaOS      string // UserAgent.osVersion
	uaBuild   string // UserAgent.osBuildNumber
	platform  waproto.DeviceProps_PlatformType
}

// desktopPool is the curated set of valid desktop fingerprints. Every entry is a
// real browser/OS pairing that WhatsApp Web supports, so any random pick is
// coherent. Add only realistic combinations here.
var desktopPool = []desktopCombo{
	{os: "Windows", browser: "Chrome", osVersion: "10.0", uaOS: "10.0.19045", uaBuild: "19045", platform: waproto.DeviceProps_CHROME},
	{os: "Windows", browser: "Edge", osVersion: "10.0", uaOS: "10.0.22631", uaBuild: "22631", platform: waproto.DeviceProps_EDGE},
	{os: "Windows", browser: "Firefox", osVersion: "10.0", uaOS: "10.0.19045", uaBuild: "19045", platform: waproto.DeviceProps_FIREFOX},
	{os: "Ubuntu", browser: "Chrome", osVersion: "22.04.4", uaOS: "0.1", uaBuild: "0.1", platform: waproto.DeviceProps_CHROME},
	{os: "Ubuntu", browser: "Firefox", osVersion: "22.04.4", uaOS: "0.1", uaBuild: "0.1", platform: waproto.DeviceProps_FIREFOX},
	{os: "Mac OS", browser: "Chrome", osVersion: "14.4.1", uaOS: "14.4.1", uaBuild: "23E224", platform: waproto.DeviceProps_CHROME},
	{os: "Mac OS", browser: "Safari", osVersion: "14.4.1", uaOS: "14.4.1", uaBuild: "23E224", platform: waproto.DeviceProps_SAFARI},
	{os: "Mac OS", browser: "Firefox", osVersion: "13.6", uaOS: "13.6", uaBuild: "22G120", platform: waproto.DeviceProps_FIREFOX},
}

// localeCombo is a coherent {lang, country, mcc, mnc} bundle. Random profiles
// pick a whole bundle so the language matches the country/carrier.
type localeCombo struct {
	lang, country, mcc, mnc string
}

var localePool = []localeCombo{
	{lang: "en", country: "US", mcc: "310", mnc: "260"},
	{lang: "en", country: "GB", mcc: "234", mnc: "10"},
	{lang: "pt", country: "BR", mcc: "724", mnc: "06"},
	{lang: "es", country: "ES", mcc: "214", mnc: "07"},
	{lang: "de", country: "DE", mcc: "262", mnc: "01"},
}

// clientVersionPool holds plausible WhatsApp Web client versions. Picking from a
// small set keeps the buildHash realistic without inventing nonexistent builds.
var clientVersionPool = [][3]uint32{
	{2, 3000, 1035194821},
	{2, 3000, 1023223821},
	{2, 3000, 1019903392},
}

// RandomDesktopProfile builds a deterministic, realistic desktop fingerprint
// from seed. Determinism (seed in -> profile out) is a hard requirement: it lets
// each instance derive a stable fingerprint (e.g. from its store id) and lets
// tests assert reproducibility. It NEVER uses the global math/rand source.
//
// Coherence guarantees:
//   - Browser × OS are always a real pairing (from desktopPool), so no Safari on
//     Windows, no Edge on macOS, etc.
//   - locale fields come as one bundle (localePool), so language matches the
//     country and carrier MCC/MNC.
func RandomDesktopProfile(seed int64) DeviceProfile {
	r := rand.New(rand.NewSource(seed))
	return randomDesktopProfileFrom(r)
}

// randomDesktopProfileFrom is the rand.Source-injectable core, so callers (and
// tests) can supply their own *rand.Rand instead of a seed.
func randomDesktopProfileFrom(r *rand.Rand) DeviceProfile {
	combo := desktopPool[r.Intn(len(desktopPool))]
	loc := localePool[r.Intn(len(localePool))]
	ver := clientVersionPool[r.Intn(len(clientVersionPool))]

	return DeviceProfile{
		Browser:       [3]string{combo.os, combo.browser, combo.osVersion},
		OSVersion:     combo.uaOS,
		Device:        "Desktop",
		OSBuildNumber: combo.uaBuild,
		ClientVersion: ver,
		Platform:      combo.platform,
		LocaleLang:    loc.lang,
		LocaleCountry: loc.country,
		MCC:           loc.mcc,
		MNC:           loc.mnc,
	}
}

// RegInput merges this profile's fingerprint into a base waproto.RegInput,
// overwriting only the fingerprint fields and leaving key material untouched.
// The caller supplies the base (with RegistrationID/keys/SyncFull set).
func (p DeviceProfile) RegInput(base waproto.RegInput) waproto.RegInput {
	base.Version = waproto.WAVersion(p.ClientVersion)
	base.Browser = waproto.Browser(p.Browser)
	base.CountryCode = p.LocaleCountry
	base.OSVersion = p.OSVersion
	base.Device = p.Device
	base.OSBuildNumber = p.OSBuildNumber
	base.LocaleLang = p.LocaleLang
	base.MCC = p.MCC
	base.MNC = p.MNC
	return base
}

// LoginInput merges this profile's fingerprint into a base waproto.LoginInput,
// leaving Username/Device (the JID parts) untouched.
func (p DeviceProfile) LoginInput(base waproto.LoginInput) waproto.LoginInput {
	base.Version = waproto.WAVersion(p.ClientVersion)
	base.CountryCode = p.LocaleCountry
	base.OSVersion = p.OSVersion
	base.DeviceName = p.Device
	base.OSBuildNumber = p.OSBuildNumber
	base.LocaleLang = p.LocaleLang
	base.MCC = p.MCC
	base.MNC = p.MNC
	return base
}
