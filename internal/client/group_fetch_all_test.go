package client

import (
	"testing"

	"github.com/jfelipesjc/wa-go/internal/wire"
)

func TestParseAllGroups(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{{
			Tag: "groups",
			Content: []wire.Node{
				{
					Tag:   "group",
					Attrs: map[string]string{"id": "111-1", "subject": "Grupo A", "creation": "1700000000"},
					Content: []wire.Node{
						{Tag: "participant", Attrs: map[string]string{"jid": "5511@s.whatsapp.net", "type": "admin"}},
						{Tag: "participant", Attrs: map[string]string{"jid": "5512@s.whatsapp.net"}},
					},
				},
				{
					Tag:   "group",
					Attrs: map[string]string{"id": "222-2", "subject": "Grupo B", "creation": "1700000100"},
					Content: []wire.Node{
						{Tag: "participant", Attrs: map[string]string{"jid": "5513@s.whatsapp.net"}},
					},
				},
			},
		}},
	}

	groups, err := parseAllGroups(reply)
	if err != nil {
		t.Fatalf("parseAllGroups: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}
	if groups[0].Subject != "Grupo A" || groups[1].Subject != "Grupo B" {
		t.Fatalf("subjects: %q, %q", groups[0].Subject, groups[1].Subject)
	}
	if len(groups[0].Participants) != 2 {
		t.Fatalf("group A participants = %d, want 2", len(groups[0].Participants))
	}
}

func TestParseAllGroupsEmpty(t *testing.T) {
	groups, err := parseAllGroups(wire.Node{Tag: "iq"})
	if err != nil {
		t.Fatalf("parseAllGroups empty: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("want 0 groups, got %d", len(groups))
	}
}
