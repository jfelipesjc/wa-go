package client

import (
	"encoding/json"
	"testing"

	"github.com/felipeleal/wa-go/internal/wire"
)

func strptr(s string) *string { return &s }

func TestNewsletterAdminQueryIDsConstants(t *testing.T) {
	// Guard against accidental edits: must match Baileys lib/Types/Mex.js.
	cases := map[string]string{
		nlQueryUpdate:      "24250201037901610",
		nlQueryAdminCount:  "7130823597031706",
		nlQueryChangeOwner: "7341777602580933",
		nlQueryDemote:      "6551828931592903",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("query id = %q, want %q", got, want)
		}
	}
	// Data paths.
	paths := map[string]string{
		nlPathUpdate:      "xwa2_newsletter_update",
		nlPathAdminCount:  "xwa2_newsletter_admin",
		nlPathChangeOwner: "xwa2_newsletter_change_owner",
		nlPathDemote:      "xwa2_newsletter_demote",
	}
	for got, want := range paths {
		if got != want {
			t.Errorf("data path = %q, want %q", got, want)
		}
	}
}

func TestNewsletterUpdateVariables(t *testing.T) {
	in := NewsletterUpdateInput{
		Name:        strptr("New Name"),
		Description: strptr("New Desc"),
	}
	v := newsletterUpdateVariables("120@newsletter", in, nil)
	if v["newsletter_id"] != "120@newsletter" {
		t.Fatalf("newsletter_id = %v", v["newsletter_id"])
	}
	updates := v["updates"].(map[string]any)
	if updates["name"] != "New Name" || updates["description"] != "New Desc" {
		t.Fatalf("updates = %v", updates)
	}
	// settings present and null on a plain edit.
	settings, ok := updates["settings"]
	if !ok {
		t.Fatalf("updates missing settings key")
	}
	if settings != nil {
		t.Errorf("settings = %v, want nil", settings)
	}
	// picture omitted when unset.
	if _, ok := updates["picture"]; ok {
		t.Errorf("picture should be omitted when unset")
	}
}

func TestNewsletterUpdateVariablesJSON(t *testing.T) {
	// settings:null must marshal to JSON null (matches Baileys settings:null).
	in := NewsletterUpdateInput{Name: strptr("X")}
	b, _ := json.Marshal(newsletterUpdateVariables("j@newsletter", in, nil))
	var decoded struct {
		NewsletterID string `json:"newsletter_id"`
		Updates      struct {
			Name     string          `json:"name"`
			Settings json.RawMessage `json:"settings"`
		} `json:"updates"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.NewsletterID != "j@newsletter" || decoded.Updates.Name != "X" {
		t.Fatalf("decoded = %+v", decoded)
	}
	if string(decoded.Updates.Settings) != "null" {
		t.Errorf("settings = %s, want null", decoded.Updates.Settings)
	}
}

func TestNewsletterUpdatePictureRemove(t *testing.T) {
	// Empty-string picture sets picture:"" (newsletterRemovePicture).
	in := NewsletterUpdateInput{Picture: strptr("")}
	v := newsletterUpdateVariables("j@newsletter", in, nil)
	updates := v["updates"].(map[string]any)
	if updates["picture"] != "" {
		t.Errorf("picture = %v, want empty string", updates["picture"])
	}
}

func TestReactionModeSettingsVariables(t *testing.T) {
	v := newsletterUpdateVariables("j@newsletter", NewsletterUpdateInput{}, reactionModeSettings(ReactionModeBlocklist))
	updates := v["updates"].(map[string]any)
	settings := updates["settings"].(map[string]any)
	rc := settings["reaction_codes"].(map[string]any)
	if rc["value"] != "BLOCKLIST" {
		t.Errorf("reaction value = %v, want BLOCKLIST", rc["value"])
	}
}

func TestNormalizeReactionMode(t *testing.T) {
	cases := map[NewsletterReactionMode]NewsletterReactionMode{
		"all":       ReactionModeAll,
		"basic":     ReactionModeBasic,
		"none":      ReactionModeNone,
		"blocklist": ReactionModeBlocklist,
		"ALL":       ReactionModeAll,
		"BLOCKLIST": ReactionModeBlocklist,
	}
	for in, want := range cases {
		if got := normalizeReactionMode(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
		if !validReactionMode(normalizeReactionMode(in)) {
			t.Errorf("normalize(%q) not valid", in)
		}
	}
	if validReactionMode("bogus") {
		t.Errorf("bogus mode should be invalid")
	}
}

func TestNewsletterUserVariables(t *testing.T) {
	v := newsletterUserVariables("j@newsletter", "9999@s.whatsapp.net")
	if v["newsletter_id"] != "j@newsletter" || v["user_id"] != "9999@s.whatsapp.net" {
		t.Fatalf("vars = %v", v)
	}
}

func TestBuildNewsletterFetchMessages(t *testing.T) {
	n := buildNewsletterFetchMessages("id-9", "j@newsletter", 50, 1700000000)
	if n.Tag != "iq" {
		t.Fatalf("tag = %q", n.Tag)
	}
	for k, want := range map[string]string{
		"id":    "id-9",
		"type":  "get",
		"xmlns": "newsletter",
		"to":    "j@newsletter",
	} {
		if n.Attrs[k] != want {
			t.Errorf("attr %s = %q, want %q", k, n.Attrs[k], want)
		}
	}
	mu, ok := childByTag(n, "message_updates")
	if !ok {
		t.Fatal("missing message_updates child")
	}
	if mu.Attrs["count"] != "50" {
		t.Errorf("count = %q, want 50", mu.Attrs["count"])
	}
	if mu.Attrs["since"] != "1700000000" {
		t.Errorf("since = %q", mu.Attrs["since"])
	}
}

func TestBuildNewsletterFetchMessagesNoSince(t *testing.T) {
	n := buildNewsletterFetchMessages("id-10", "j@newsletter", 10, 0)
	mu, _ := childByTag(n, "message_updates")
	if _, ok := mu.Attrs["since"]; ok {
		t.Errorf("since should be omitted when <=0")
	}
}

func TestBuildSubscribeLiveUpdates(t *testing.T) {
	n := buildSubscribeLiveUpdates("id-11", "j@newsletter")
	for k, want := range map[string]string{
		"id":    "id-11",
		"type":  "set",
		"xmlns": "newsletter",
		"to":    "j@newsletter",
	} {
		if n.Attrs[k] != want {
			t.Errorf("attr %s = %q, want %q", k, n.Attrs[k], want)
		}
	}
	if _, ok := childByTag(n, "live_updates"); !ok {
		t.Fatal("missing live_updates child")
	}
}

func TestParseAdminCount(t *testing.T) {
	// number form
	if n, err := parseAdminCount(json.RawMessage(`{"admin_count":3}`)); err != nil || n != 3 {
		t.Fatalf("number form: n=%d err=%v", n, err)
	}
	// string form
	if n, err := parseAdminCount(json.RawMessage(`{"admin_count":"7"}`)); err != nil || n != 7 {
		t.Fatalf("string form: n=%d err=%v", n, err)
	}
	// missing
	if _, err := parseAdminCount(json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected error for missing admin_count")
	}
}

func TestParseNewsletterMessages(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{Tag: "messages", Content: []wire.Node{
				{
					Tag:     "message",
					Attrs:   map[string]string{"server_id": "101", "t": "1700000001", "type": "text"},
					Content: []byte("payload-1"),
				},
				{
					Tag:     "message",
					Attrs:   map[string]string{"server_id": "102", "t": "1700000002", "type": "media"},
					Content: []byte("payload-2"),
				},
			}},
		},
	}
	msgs := parseNewsletterMessages(reply)
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0].ServerID != "101" || msgs[0].Timestamp != 1700000001 || msgs[0].Type != "text" {
		t.Errorf("msg0 = %+v", msgs[0])
	}
	if string(msgs[0].Content) != "payload-1" {
		t.Errorf("msg0 content = %s", msgs[0].Content)
	}
	if msgs[1].ServerID != "102" || msgs[1].Timestamp != 1700000002 {
		t.Errorf("msg1 = %+v", msgs[1])
	}
}

func TestParseNewsletterMessagesDirectChildren(t *testing.T) {
	// Layout where <message> nodes are direct children of the iq.
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{Tag: "message", Attrs: map[string]string{"server_id": "55"}, Content: []byte("x")},
		},
	}
	msgs := parseNewsletterMessages(reply)
	if len(msgs) != 1 || msgs[0].ServerID != "55" {
		t.Fatalf("msgs = %+v", msgs)
	}
}

func TestNewsletterUpdateInputIsEmpty(t *testing.T) {
	if !(NewsletterUpdateInput{}).IsEmpty() {
		t.Error("zero input should be empty")
	}
	if (NewsletterUpdateInput{Name: strptr("x")}).IsEmpty() {
		t.Error("input with name should not be empty")
	}
}
