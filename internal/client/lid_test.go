package client

import (
	"path/filepath"
	"testing"

	"github.com/felipeleal/wa-go/internal/store"
	"github.com/felipeleal/wa-go/internal/wire"
)

func TestJIDServerAndKindHelpers(t *testing.T) {
	cases := []struct {
		jid    string
		server string
		isLID  bool
		isPN   bool
	}{
		{"551199990000@s.whatsapp.net", "s.whatsapp.net", false, true},
		{"551199990000:3@s.whatsapp.net", "s.whatsapp.net", false, true},
		{"123456789@lid", "lid", true, false},
		{"123456789:2@lid", "lid", true, false},
		{"120363000000000000@g.us", "g.us", false, false},
		{"551199990000", "s.whatsapp.net", false, true}, // bare, defaults to PN server
	}
	for _, c := range cases {
		if got := jidServer(c.jid); got != c.server {
			t.Errorf("jidServer(%q) = %q, want %q", c.jid, got, c.server)
		}
		if got := isLID(c.jid); got != c.isLID {
			t.Errorf("isLID(%q) = %v, want %v", c.jid, got, c.isLID)
		}
		if got := isPN(c.jid); got != c.isPN {
			t.Errorf("isPN(%q) = %v, want %v", c.jid, got, c.isPN)
		}
	}
}

func TestNormalizeJID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"551199990000:3@s.whatsapp.net", "551199990000@s.whatsapp.net"},
		{"551199990000@s.whatsapp.net", "551199990000@s.whatsapp.net"},
		{"123456789:2@lid", "123456789@lid"},
		{"551199990000@c.us", "551199990000@s.whatsapp.net"}, // legacy server mapped
		{"barewithoutat", "barewithoutat"},
	}
	for _, c := range cases {
		if got := normalizeJID(c.in); got != c.want {
			t.Errorf("normalizeJID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSignalAddressAcceptsLIDAndPN(t *testing.T) {
	cases := []struct {
		jid  string
		want string
	}{
		{"551199990000@s.whatsapp.net", "551199990000.0"},
		{"551199990000:5@s.whatsapp.net", "551199990000.5"},
		{"123456789@lid", "123456789.0"},
		{"123456789:2@lid", "123456789.2"},
	}
	for _, c := range cases {
		got, err := signalAddress(c.jid)
		if err != nil {
			t.Fatalf("signalAddress(%q): %v", c.jid, err)
		}
		if got != c.want {
			t.Errorf("signalAddress(%q) = %q, want %q", c.jid, got, c.want)
		}
	}
}

func newTestSQLite(t *testing.T) store.Store {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "lid.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestLIDStoreRoundTripMemoryOnly(t *testing.T) {
	ls := newLIDStore(nil)
	const lid = "123456789@lid"
	const pn = "551199990000@s.whatsapp.net"
	if err := ls.MapLIDToPN(lid, pn); err != nil {
		t.Fatalf("MapLIDToPN: %v", err)
	}

	if got, ok := ls.PNForLID(lid); !ok || got != pn {
		t.Errorf("PNForLID(%q) = %q,%v want %q,true", lid, got, ok, pn)
	}
	if got, ok := ls.LIDForPN(pn); !ok || got != lid {
		t.Errorf("LIDForPN(%q) = %q,%v want %q,true", pn, got, ok, lid)
	}

	// Device index is preserved when re-attached.
	if got, ok := ls.PNForLID("123456789:4@lid"); !ok || got != "551199990000:4@s.whatsapp.net" {
		t.Errorf("PNForLID device-preserve = %q,%v", got, ok)
	}
	if got, ok := ls.LIDForPN("551199990000:7@s.whatsapp.net"); !ok || got != "123456789:7@lid" {
		t.Errorf("LIDForPN device-preserve = %q,%v", got, ok)
	}

	// Misses.
	if _, ok := ls.PNForLID("999@lid"); ok {
		t.Error("PNForLID miss should be ok=false")
	}
	if _, ok := ls.LIDForPN("888@s.whatsapp.net"); ok {
		t.Error("LIDForPN miss should be ok=false")
	}
}

func TestLIDStoreInvalidMapping(t *testing.T) {
	ls := newLIDStore(nil)
	if err := ls.MapLIDToPN("123@s.whatsapp.net", "456@s.whatsapp.net"); err == nil {
		t.Error("expected error mapping PN as LID")
	}
	if err := ls.MapLIDToPN("123@lid", "456@lid"); err == nil {
		t.Error("expected error mapping LID as PN")
	}
}

func TestLIDStorePersistence(t *testing.T) {
	st := newTestSQLite(t)
	const lid = "777888999@lid"
	const pn = "5521988887777@s.whatsapp.net"

	// Write through one store...
	ls1 := newLIDStore(st)
	if err := ls1.MapLIDToPN(lid, pn); err != nil {
		t.Fatalf("MapLIDToPN: %v", err)
	}

	// ...and read back through a fresh in-memory cache over the same backend.
	ls2 := newLIDStore(st)
	if got, ok := ls2.PNForLID(lid); !ok || got != pn {
		t.Errorf("persisted PNForLID = %q,%v want %q,true", got, ok, pn)
	}
	ls3 := newLIDStore(st)
	if got, ok := ls3.LIDForPN(pn); !ok || got != lid {
		t.Errorf("persisted LIDForPN = %q,%v want %q,true", got, ok, lid)
	}

	// Direct store-level round trip.
	if got, ok, err := st.LoadPNForLID("777888999"); err != nil || !ok || got != "5521988887777" {
		t.Errorf("store LoadPNForLID = %q,%v,%v", got, ok, err)
	}
	if got, ok, err := st.LoadLIDForPN("5521988887777"); err != nil || !ok || got != "777888999" {
		t.Errorf("store LoadLIDForPN = %q,%v,%v", got, ok, err)
	}
}

func TestParseUSyncLID(t *testing.T) {
	// Reply with both shapes: one <user> carrying lid as an attribute, one with
	// the LID as a <lid val=...> child.
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{
				Tag: "usync",
				Content: []wire.Node{
					{
						Tag: "list",
						Content: []wire.Node{
							{
								Tag:   "user",
								Attrs: map[string]string{"jid": "551199990000@s.whatsapp.net", "lid": "123456789@lid"},
							},
							{
								Tag:   "user",
								Attrs: map[string]string{"jid": "5521988887777@s.whatsapp.net"},
								Content: []wire.Node{
									{Tag: "lid", Attrs: map[string]string{"val": "777888999@lid"}},
								},
							},
							{
								// No LID resolved -> skipped.
								Tag:   "user",
								Attrs: map[string]string{"jid": "5511000000000@s.whatsapp.net"},
							},
						},
					},
				},
			},
		},
	}
	pairs, err := parseUSyncLID(reply)
	if err != nil {
		t.Fatalf("parseUSyncLID: %v", err)
	}
	if len(pairs) != 2 {
		t.Fatalf("got %d pairs, want 2: %+v", len(pairs), pairs)
	}
	want := map[string]string{
		"551199990000@s.whatsapp.net":  "123456789@lid",
		"5521988887777@s.whatsapp.net": "777888999@lid",
	}
	for _, p := range pairs {
		if want[p.PhoneJID] != p.LID {
			t.Errorf("pair %+v not in expected set", p)
		}
	}
}

func TestParseUSyncLIDMissingNodes(t *testing.T) {
	if _, err := parseUSyncLID(wire.Node{Tag: "iq"}); err == nil {
		t.Error("expected error on reply missing <usync>")
	}
}

func TestUSyncLIDQueryNodeShape(t *testing.T) {
	n := usyncLIDQueryNode("id1", "sid1", []string{"551199990000@s.whatsapp.net"})
	if n.Tag != "iq" || n.Attrs["xmlns"] != "usync" {
		t.Fatalf("unexpected iq shape: %+v", n.Attrs)
	}
	usync, ok := childByTag(n, "usync")
	if !ok {
		t.Fatal("missing <usync>")
	}
	query, ok := childByTag(usync, "query")
	if !ok {
		t.Fatal("missing <query>")
	}
	if _, ok := childByTag(query, "lid"); !ok {
		t.Error("query must contain <lid>")
	}
	list, ok := childByTag(usync, "list")
	if !ok {
		t.Fatal("missing <list>")
	}
	users := childrenByTag(list, "user")
	if len(users) != 1 || users[0].Attrs["jid"] != "551199990000@s.whatsapp.net" {
		t.Errorf("unexpected user list: %+v", users)
	}
}

func TestRegisterAddressingMappingFromStanza(t *testing.T) {
	c := &Client{store: newTestSQLite(t)}

	// LID-addressed stanza: sender is @lid, PN counterpart in participant_pn.
	c.registerAddressingMapping(map[string]string{
		"from":            "120363000000000000@g.us",
		"participant":     "123456789@lid",
		"addressing_mode": "lid",
		"participant_pn":  "551199990000@s.whatsapp.net",
	})
	if got, ok := c.PNForLID("123456789@lid"); !ok || got != "551199990000@s.whatsapp.net" {
		t.Errorf("after LID stanza PNForLID = %q,%v", got, ok)
	}

	// PN-addressed 1:1 stanza carrying the LID counterpart in sender_lid.
	c.registerAddressingMapping(map[string]string{
		"from":       "5521988887777@s.whatsapp.net",
		"sender_lid": "777888999@lid",
	})
	if got, ok := c.lidStore().LIDForPN("5521988887777@s.whatsapp.net"); !ok || got != "777888999@lid" {
		t.Errorf("after PN stanza LIDForPN = %q,%v", got, ok)
	}

	// Stanza with no counterpart attrs records nothing (no panic / no false map).
	c.registerAddressingMapping(map[string]string{"from": "5511222223333@s.whatsapp.net"})
	if _, ok := c.lidStore().LIDForPN("5511222223333@s.whatsapp.net"); ok {
		t.Error("expected no mapping for stanza without counterpart")
	}
}
