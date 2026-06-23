package client

import "testing"

// buildLocationMessage must carry the coordinates and only set name/address when
// non-empty (oneof fields stay nil otherwise).
func TestBuildLocationMessage(t *testing.T) {
	m := buildLocationMessage(-23.5505, -46.6333, "Sé", "Praça da Sé, SP")
	loc := m.GetLocationMessage()
	if loc == nil {
		t.Fatal("missing locationMessage")
	}
	if loc.GetDegreesLatitude() != -23.5505 || loc.GetDegreesLongitude() != -46.6333 {
		t.Fatalf("coords wrong: %v,%v", loc.GetDegreesLatitude(), loc.GetDegreesLongitude())
	}
	if loc.GetName() != "Sé" || loc.GetAddress() != "Praça da Sé, SP" {
		t.Fatalf("labels wrong: name=%q address=%q", loc.GetName(), loc.GetAddress())
	}

	// Without labels the oneof fields stay nil (not empty strings).
	bare := buildLocationMessage(1.0, 2.0, "", "")
	bloc := bare.GetLocationMessage()
	if bloc.Name != nil || bloc.Address != nil {
		t.Fatalf("empty labels should be nil: name=%v address=%v", bloc.Name, bloc.Address)
	}
	if bloc.GetDegreesLatitude() != 1.0 || bloc.GetDegreesLongitude() != 2.0 {
		t.Fatalf("bare coords wrong: %v,%v", bloc.GetDegreesLatitude(), bloc.GetDegreesLongitude())
	}
}

// buildContactMessage must carry the vCard and only set displayName when present.
func TestBuildContactMessage(t *testing.T) {
	const vcard = "BEGIN:VCARD\nVERSION:3.0\nFN:Jane\nTEL:+5511999999999\nEND:VCARD"
	m := buildContactMessage("Jane Doe", vcard)
	cm := m.GetContactMessage()
	if cm == nil {
		t.Fatal("missing contactMessage")
	}
	if cm.GetDisplayName() != "Jane Doe" {
		t.Fatalf("displayName = %q", cm.GetDisplayName())
	}
	if cm.GetVcard() != vcard {
		t.Fatalf("vcard = %q", cm.GetVcard())
	}

	bare := buildContactMessage("", vcard)
	if bare.GetContactMessage().DisplayName != nil {
		t.Fatal("empty displayName should be nil")
	}
}
