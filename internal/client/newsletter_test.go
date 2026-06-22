package client

import (
	"encoding/json"
	"testing"

	"github.com/felipeleal/wa-go/internal/wire"
)

// queryBody returns the decoded {"variables":...} JSON of a built w:mex iq.
func queryBody(t *testing.T, n wire.Node) map[string]any {
	t.Helper()
	if n.Tag != "iq" {
		t.Fatalf("root tag = %q, want iq", n.Tag)
	}
	q, ok := childByTag(n, "query")
	if !ok {
		t.Fatalf("iq missing <query> child")
	}
	raw := nodeBytes(q)
	if len(raw) == 0 {
		t.Fatalf("<query> has no JSON body")
	}
	var wrapper struct {
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		t.Fatalf("decode variables: %v", err)
	}
	return wrapper.Variables
}

func TestBuildMexQueryEnvelope(t *testing.T) {
	n, err := buildMexQuery("id-1", nlQueryFollow, newsletterIDVariables("123@newsletter"))
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"id":    "id-1",
		"xmlns": "w:mex",
		"type":  "get",
		"to":    sWhatsAppNet,
	} {
		if n.Attrs[k] != want {
			t.Errorf("attr %s = %q, want %q", k, n.Attrs[k], want)
		}
	}
	q, ok := childByTag(n, "query")
	if !ok {
		t.Fatal("missing <query>")
	}
	if q.Attrs["query_id"] != nlQueryFollow {
		t.Errorf("query_id = %q, want %q", q.Attrs["query_id"], nlQueryFollow)
	}
	vars := queryBody(t, n)
	if vars["newsletter_id"] != "123@newsletter" {
		t.Errorf("newsletter_id = %v", vars["newsletter_id"])
	}
}

func TestNewsletterQueryIDsConstants(t *testing.T) {
	// Guard against accidental edits: these must match Baileys lib/Types/Mex.js.
	cases := map[string]string{
		nlQueryCreate:   "8823471724422422",
		nlQueryMetadata: "6563316087068696",
		nlQueryFollow:   "24404358912487870",
		nlQueryUnfollow: "9767147403369991",
		nlQueryMute:     "29766401636284406",
		nlQueryUnmute:   "9864994326891137",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("query id = %q, want %q", got, want)
		}
	}
}

func TestNewsletterCreateVariables(t *testing.T) {
	// With description.
	v := newsletterCreateVariables("My Channel", "hello")
	input := v["input"].(map[string]any)
	if input["name"] != "My Channel" || input["description"] != "hello" {
		t.Fatalf("input = %v", input)
	}
	// Empty description -> JSON null.
	b, _ := json.Marshal(newsletterCreateVariables("X", ""))
	var decoded map[string]map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["input"]["description"] != nil {
		t.Errorf("empty description should marshal to null, got %v", decoded["input"]["description"])
	}
}

func TestNewsletterMetadataVariables(t *testing.T) {
	v := newsletterMetadataVariables("inv123", NewsletterKeyInvite)
	if v["fetch_creation_time"] != true || v["fetch_full_image"] != true || v["fetch_viewer_metadata"] != true {
		t.Fatalf("fetch flags not all true: %v", v)
	}
	input := v["input"].(map[string]any)
	if input["key"] != "inv123" || input["type"] != "INVITE" {
		t.Fatalf("input = %v", input)
	}
}

// mexReply builds a synthetic w:mex reply iq with the given <result> JSON.
func mexReply(jsonBody string) wire.Node {
	return wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{Tag: "result", Content: []byte(jsonBody)},
		},
	}
}

func TestExtractMexData(t *testing.T) {
	reply := mexReply(`{"data":{"xwa2_newsletter":{"id":"abc@newsletter"}}}`)
	raw, err := extractMexData(reply, "xwa2_newsletter")
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"id":"abc@newsletter"}` {
		t.Errorf("raw = %s", raw)
	}
}

func TestExtractMexDataError(t *testing.T) {
	reply := mexReply(`{"errors":[{"message":"nope","extensions":{"error_code":403}}]}`)
	_, err := extractMexData(reply, "xwa2_newsletter")
	if err == nil {
		t.Fatal("expected error from GraphQL errors array")
	}
}

func TestParseNewsletter(t *testing.T) {
	payload := `{
		"id":"120363@newsletter",
		"thread_metadata":{
			"name":{"text":"Canal X"},
			"description":{"text":"desc here"},
			"invite":"AbCdInvite",
			"subscribers_count":"4321",
			"verification":"VERIFIED",
			"creation_time":"1700000000"
		},
		"viewer_metadata":{"mute":"OFF"}
	}`
	info, err := parseNewsletter(json.RawMessage(payload))
	if err != nil {
		t.Fatal(err)
	}
	if info.JID != "120363@newsletter" || info.Name != "Canal X" || info.Description != "desc here" {
		t.Errorf("info = %+v", info)
	}
	if info.Invite != "AbCdInvite" || info.SubscriberCount != 4321 {
		t.Errorf("invite/subs = %+v", info)
	}
	if info.Verification != "VERIFIED" || info.CreationTime != 1700000000 || info.MuteState != "OFF" {
		t.Errorf("verif/creation/mute = %+v", info)
	}
}

func TestParseNewsletterResultWrapped(t *testing.T) {
	// parseNewsletterMetadata unwraps a {"result": {...}} envelope.
	payload := `{"result":{"id":"w@newsletter","thread_metadata":{"name":{"text":"Y"}}}}`
	info, err := parseNewsletter(json.RawMessage(payload))
	if err != nil {
		t.Fatal(err)
	}
	if info.JID != "w@newsletter" || info.Name != "Y" {
		t.Errorf("info = %+v", info)
	}
}

func TestParseNewsletterMissingID(t *testing.T) {
	if _, err := parseNewsletter(json.RawMessage(`{"thread_metadata":{}}`)); err == nil {
		t.Fatal("expected error for missing id")
	}
}
