package client

import (
	"testing"

	"github.com/jfelipesjc/wa-go/internal/wire"
)

// --- call.go ---

func TestRejectCallNode(t *testing.T) {
	n := rejectCallNode("me@s.whatsapp.net", "CALL123", "555@s.whatsapp.net")
	if n.Tag != "call" {
		t.Fatalf("tag = %q", n.Tag)
	}
	attr(t, n, "from", "me@s.whatsapp.net")
	attr(t, n, "to", "555@s.whatsapp.net")
	reject, ok := childByTag(n, "reject")
	if !ok {
		t.Fatal("missing <reject>")
	}
	attr(t, reject, "call-id", "CALL123")
	attr(t, reject, "call-creator", "555@s.whatsapp.net")
	attr(t, reject, "count", "0")
	if children(reject) != nil {
		t.Fatal("<reject> must be a leaf")
	}
}

func TestParseCallNode_Offer(t *testing.T) {
	node := wire.Node{
		Tag:   "call",
		Attrs: map[string]string{"from": "555@s.whatsapp.net", "t": "1700000000"},
		Content: []wire.Node{
			{
				Tag: "offer",
				Attrs: map[string]string{
					"call-id":      "CALLAAA",
					"call-creator": "555@s.whatsapp.net",
					"caller_pn":    "5511999@s.whatsapp.net",
				},
				Content: []wire.Node{
					{Tag: "video"},
				},
			},
		},
	}
	info, err := parseCallNode(node)
	if err != nil {
		t.Fatalf("parseCallNode: %v", err)
	}
	if info.ID != "CALLAAA" {
		t.Fatalf("ID = %q", info.ID)
	}
	if info.From != "555@s.whatsapp.net" {
		t.Fatalf("From = %q", info.From)
	}
	if info.Stage != CallStageOffer || !info.Offer {
		t.Fatalf("stage = %q offer=%v", info.Stage, info.Offer)
	}
	if !info.Video {
		t.Fatal("expected Video=true")
	}
	if info.Group {
		t.Fatal("expected Group=false")
	}
	if info.Timestamp != 1700000000 {
		t.Fatalf("Timestamp = %d", info.Timestamp)
	}
	if info.CallerPN != "5511999@s.whatsapp.net" {
		t.Fatalf("CallerPN = %q", info.CallerPN)
	}
}

func TestParseCallNode_FromFallbacks(t *testing.T) {
	// No info.from -> falls back to call-creator.
	node := wire.Node{
		Tag:   "call",
		Attrs: map[string]string{"from": "top@s.whatsapp.net"},
		Content: []wire.Node{
			{Tag: "terminate", Attrs: map[string]string{"call-id": "X", "call-creator": "creator@s.whatsapp.net"}},
		},
	}
	info, err := parseCallNode(node)
	if err != nil {
		t.Fatal(err)
	}
	if info.From != "creator@s.whatsapp.net" {
		t.Fatalf("From = %q (want call-creator)", info.From)
	}
	if info.Stage != CallStageTerminate {
		t.Fatalf("stage = %q", info.Stage)
	}
	if info.Offer || info.Video {
		t.Fatal("terminate must not be offer/video")
	}

	// No info.from and no call-creator -> falls back to top-level call from.
	node2 := wire.Node{
		Tag:     "call",
		Attrs:   map[string]string{"from": "top@s.whatsapp.net"},
		Content: []wire.Node{{Tag: "offer", Attrs: map[string]string{"call-id": "Y", "type": "group"}}},
	}
	info2, err := parseCallNode(node2)
	if err != nil {
		t.Fatal(err)
	}
	if info2.From != "top@s.whatsapp.net" {
		t.Fatalf("From = %q (want top-level)", info2.From)
	}
	if !info2.Group {
		t.Fatal("expected Group=true")
	}
}

func TestParseCallNode_Errors(t *testing.T) {
	if _, err := parseCallNode(wire.Node{Tag: "message"}); err == nil {
		t.Fatal("expected error for non-call node")
	}
	if _, err := parseCallNode(wire.Node{Tag: "call"}); err == nil {
		t.Fatal("expected error for call with no info child")
	}
}

func TestCallEvent_IsEvent(t *testing.T) {
	var _ Event = CallEvent{}
}

// --- status.go ---

func TestBuildStatusMessage(t *testing.T) {
	m := buildStatusMessage("hi there")
	if m.GetExtendedTextMessage() == nil {
		t.Fatal("status must use ExtendedTextMessage")
	}
	if got := m.GetExtendedTextMessage().GetText(); got != "hi there" {
		t.Fatalf("text = %q", got)
	}
}

func TestStatusRecipients_DedupTrim(t *testing.T) {
	got := statusRecipients([]string{" a@s ", "b@s", "a@s", "", "  "})
	want := []string{"a@s", "b@s"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestBuildStatusStanza(t *testing.T) {
	participants := []wire.Node{
		{Tag: "to", Attrs: map[string]string{"jid": "d0"}, Content: []wire.Node{
			{Tag: "enc", Attrs: map[string]string{"v": "2", "type": "pkmsg"}, Content: []byte{1}},
		}},
	}
	n := buildStatusStanza("MID", participants, 1, []byte{0xaa})
	attr(t, n, "to", statusBroadcastJID)
	attr(t, n, "type", "media")
	attr(t, n, "id", "MID")
	if _, ok := childByTag(n, "participants"); !ok {
		t.Fatal("missing <participants>")
	}
	if _, ok := childByTag(n, "meta"); !ok {
		t.Fatal("missing <meta>")
	}
	// pkmsg present + account -> device-identity attached.
	if _, ok := childByTag(n, "device-identity"); !ok {
		t.Fatal("expected <device-identity> when pkmsg present")
	}
}

func TestBuildStatusStanza_NoDeviceIdentityWithoutPkmsg(t *testing.T) {
	participants := []wire.Node{
		{Tag: "to", Attrs: map[string]string{"jid": "d0"}, Content: []wire.Node{
			{Tag: "enc", Attrs: map[string]string{"v": "2", "type": "msg"}, Content: []byte{1}},
		}},
	}
	n := buildStatusStanza("MID", participants, 1, []byte{0xaa})
	if _, ok := childByTag(n, "device-identity"); ok {
		t.Fatal("no pkmsg -> must not attach device-identity")
	}
}

func TestFetchPrivacyNode(t *testing.T) {
	n := fetchPrivacyNode("p1")
	attr(t, n, "to", sWhatsAppNet)
	attr(t, n, "type", "get")
	attr(t, n, "xmlns", "privacy")
	attr(t, n, "id", "p1")
	if _, ok := childByTag(n, "privacy"); !ok {
		t.Fatal("missing <privacy>")
	}
}

func TestParseStatusPrivacy(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{Tag: "privacy", Content: []wire.Node{
				{Tag: "category", Attrs: map[string]string{"name": "last", "value": "all"}},
				{Tag: "category", Attrs: map[string]string{"name": "status", "value": "contacts"}},
			}},
		},
	}
	if got := parseStatusPrivacy(reply); got != "contacts" {
		t.Fatalf("status privacy = %q", got)
	}
	if got := parseStatusPrivacy(wire.Node{Tag: "iq"}); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

// --- business.go ---

func TestBusinessProfileNode(t *testing.T) {
	n := businessProfileNode("b1", "555@s.whatsapp.net")
	attr(t, n, "to", sWhatsAppNet)
	attr(t, n, "type", "get")
	attr(t, n, "xmlns", "w:biz")
	bp, ok := childByTag(n, "business_profile")
	if !ok {
		t.Fatal("missing <business_profile>")
	}
	attr(t, bp, "v", "244")
	prof, ok := childByTag(bp, "profile")
	if !ok {
		t.Fatal("missing <profile>")
	}
	attr(t, prof, "jid", "555@s.whatsapp.net")
}

func TestCatalogNode(t *testing.T) {
	n := catalogNode("b2", "555@s.whatsapp.net", 5)
	attr(t, n, "xmlns", "w:biz:catalog")
	pc, ok := childByTag(n, "product_catalog")
	if !ok {
		t.Fatal("missing <product_catalog>")
	}
	attr(t, pc, "jid", "555@s.whatsapp.net")
	attr(t, pc, "allow_shop_source", "true")
	if got := childText(pc, "limit"); got != "5" {
		t.Fatalf("limit = %q", got)
	}
	// non-positive limit -> default 10.
	n2 := catalogNode("b2b", "x", 0)
	pc2, _ := childByTag(n2, "product_catalog")
	if got := childText(pc2, "limit"); got != "10" {
		t.Fatalf("default limit = %q", got)
	}
}

func TestOrderDetailsNode(t *testing.T) {
	n := orderDetailsNode("b3", "ORDER1", "dG9rZW4=")
	attr(t, n, "xmlns", "fb:thrift_iq")
	attr(t, n, "smax_id", "5")
	order, ok := childByTag(n, "order")
	if !ok {
		t.Fatal("missing <order>")
	}
	attr(t, order, "op", "get")
	attr(t, order, "id", "ORDER1")
	if got := childText(order, "token"); got != "dG9rZW4=" {
		t.Fatalf("token = %q", got)
	}
	if _, ok := childByTag(order, "image_dimensions"); !ok {
		t.Fatal("missing <image_dimensions>")
	}
}

func TestParseBusinessProfile(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{Tag: "business_profile", Content: []wire.Node{
				{Tag: "profile", Attrs: map[string]string{"jid": "555@s.whatsapp.net"}, Content: []wire.Node{
					{Tag: "address", Content: []byte("Rua A, 1")},
					{Tag: "description", Content: []byte("Best shop")},
					{Tag: "website", Content: []byte("https://x.example")},
					{Tag: "email", Content: []byte("a@x.example")},
					{Tag: "business_hours", Attrs: map[string]string{"timezone": "America/Sao_Paulo"}},
					{Tag: "categories", Content: []wire.Node{
						{Tag: "category", Content: []byte("Retail")},
						{Tag: "category", Content: []byte("Electronics")},
					}},
				}},
			}},
		},
	}
	bp, err := parseBusinessProfile(reply)
	if err != nil {
		t.Fatal(err)
	}
	if bp.JID != "555@s.whatsapp.net" || bp.Address != "Rua A, 1" || bp.Description != "Best shop" {
		t.Fatalf("bp = %+v", bp)
	}
	if bp.Website != "https://x.example" || bp.Email != "a@x.example" {
		t.Fatalf("bp = %+v", bp)
	}
	if bp.BusinessHoursTimezone != "America/Sao_Paulo" {
		t.Fatalf("tz = %q", bp.BusinessHoursTimezone)
	}
	if len(bp.Categories) != 2 || bp.Categories[0] != "Retail" || bp.Categories[1] != "Electronics" {
		t.Fatalf("categories = %v", bp.Categories)
	}

	if _, err := parseBusinessProfile(wire.Node{Tag: "iq"}); err == nil {
		t.Fatal("expected error for missing business_profile")
	}
}

func TestParseCatalog(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{Tag: "product_catalog", Content: []wire.Node{
				{Tag: "product", Attrs: map[string]string{"id": "P1"}, Content: []wire.Node{
					{Tag: "name", Content: []byte("Widget")},
					{Tag: "price", Content: []byte("1990")},
					{Tag: "currency", Content: []byte("BRL")},
					{Tag: "retailer_id", Content: []byte("R-1")},
				}},
				{Tag: "product", Attrs: map[string]string{"id": "P2"}, Content: []wire.Node{
					{Tag: "name", Content: []byte("Gadget")},
				}},
			}},
		},
	}
	prods, err := parseCatalog(reply)
	if err != nil {
		t.Fatal(err)
	}
	if len(prods) != 2 {
		t.Fatalf("got %d products", len(prods))
	}
	if prods[0].ID != "P1" || prods[0].Name != "Widget" || prods[0].Price != 1990 || prods[0].Currency != "BRL" {
		t.Fatalf("p0 = %+v", prods[0])
	}
	if prods[0].RetailerID != "R-1" {
		t.Fatalf("retailer = %q", prods[0].RetailerID)
	}
	if prods[1].Price != 0 || prods[1].PriceRaw != "" {
		t.Fatalf("p1 price should be zero: %+v", prods[1])
	}

	if _, err := parseCatalog(wire.Node{Tag: "iq"}); err == nil {
		t.Fatal("expected error for missing product_catalog")
	}
}

func TestParseOrderDetails(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{Tag: "order", Content: []wire.Node{
				{Tag: "products", Content: []wire.Node{
					{Tag: "product", Attrs: map[string]string{"id": "P1"}, Content: []wire.Node{
						{Tag: "name", Content: []byte("Widget")},
						{Tag: "price", Content: []byte("1990")},
						{Tag: "currency", Content: []byte("BRL")},
						{Tag: "quantity", Content: []byte("3")},
					}},
				}},
				{Tag: "price", Content: []wire.Node{
					{Tag: "total", Content: []byte("5970")},
					{Tag: "currency", Content: []byte("BRL")},
				}},
			}},
		},
	}
	od, err := parseOrderDetails(reply)
	if err != nil {
		t.Fatal(err)
	}
	if len(od.Products) != 1 || od.Products[0].Quantity != 3 {
		t.Fatalf("products = %+v", od.Products)
	}
	if od.Total != 5970 || od.TotalCurrency != "BRL" {
		t.Fatalf("total = %d %q", od.Total, od.TotalCurrency)
	}

	if _, err := parseOrderDetails(wire.Node{Tag: "iq"}); err == nil {
		t.Fatal("expected error for missing order")
	}
}

// --- usync_query.go ---

func TestNormalisePhone(t *testing.T) {
	cases := map[string]string{
		" 5511999 ": "+5511999",
		"+5511999":  "+5511999",
		"++5511999": "+5511999",
		"":          "",
		"   ":       "",
	}
	for in, want := range cases {
		if got := normalisePhone(in); got != want {
			t.Fatalf("normalisePhone(%q) = %q want %q", in, got, want)
		}
	}
}

func TestUsyncContactQueryNode(t *testing.T) {
	n := usyncContactQueryNode("id1", "sid1", []string{"+5511999", "+5511888"})
	attr(t, n, "to", sWhatsAppNet)
	attr(t, n, "type", "get")
	attr(t, n, "xmlns", "usync")
	attr(t, n, "id", "id1")
	usync, ok := childByTag(n, "usync")
	if !ok {
		t.Fatal("missing <usync>")
	}
	attr(t, usync, "context", "interactive")
	attr(t, usync, "mode", "query")
	attr(t, usync, "sid", "sid1")
	query, ok := childByTag(usync, "query")
	if !ok {
		t.Fatal("missing <query>")
	}
	if _, ok := childByTag(query, "contact"); !ok {
		t.Fatal("missing <query><contact>")
	}
	list, ok := childByTag(usync, "list")
	if !ok {
		t.Fatal("missing <list>")
	}
	users := childrenByTag(list, "user")
	if len(users) != 2 {
		t.Fatalf("got %d users", len(users))
	}
	c0, ok := childByTag(users[0], "contact")
	if !ok {
		t.Fatal("user missing <contact>")
	}
	if string(nodeBytes(c0)) != "+5511999" {
		t.Fatalf("contact = %q", nodeBytes(c0))
	}
}

func TestParseOnWhatsApp(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{Tag: "usync", Content: []wire.Node{
				{Tag: "list", Content: []wire.Node{
					{Tag: "user", Attrs: map[string]string{"jid": "5511999@s.whatsapp.net"}, Content: []wire.Node{
						{Tag: "contact", Attrs: map[string]string{"type": "in"}, Content: []byte("+5511999")},
					}},
					{Tag: "user", Content: []wire.Node{
						{Tag: "contact", Attrs: map[string]string{"type": "out"}, Content: []byte("+5511888")},
					}},
				}},
			}},
		},
	}
	res, err := parseOnWhatsApp(reply)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d results", len(res))
	}
	if !res[0].Exists || res[0].JID != "5511999@s.whatsapp.net" || res[0].ContactType != "in" {
		t.Fatalf("res0 = %+v", res[0])
	}
	if res[0].Query != "+5511999" {
		t.Fatalf("res0.Query = %q", res[0].Query)
	}
	if res[1].Exists || res[1].JID != "" {
		t.Fatalf("res1 should not exist: %+v", res[1])
	}

	if _, err := parseOnWhatsApp(wire.Node{Tag: "iq"}); err == nil {
		t.Fatal("expected error for missing usync")
	}
}
