package client

import "testing"

func TestBuildCallOfferAudio(t *testing.T) {
	n := buildCallOffer("ID1", "me@s.whatsapp.net", "5551@s.whatsapp.net", "CALL1", false)
	if n.Tag != "call" {
		t.Fatalf("tag = %q", n.Tag)
	}
	if n.Attrs["to"] != "5551@s.whatsapp.net" || n.Attrs["id"] != "ID1" {
		t.Fatalf("call attrs wrong: %+v", n.Attrs)
	}
	offer, ok := childByTag(n, "offer")
	if !ok {
		t.Fatal("missing <offer>")
	}
	if offer.Attrs["call-id"] != "CALL1" || offer.Attrs["call-creator"] != "me@s.whatsapp.net" {
		t.Fatalf("offer attrs wrong: %+v", offer.Attrs)
	}
	if _, ok := childByTag(offer, "audio"); !ok {
		t.Fatal("missing <audio> descriptor")
	}
	if _, ok := childByTag(offer, "video"); ok {
		t.Fatal("audio-only offer should not carry <video>")
	}
	if _, ok := childByTag(offer, "encopt"); !ok {
		t.Fatal("missing <encopt>")
	}
}

func TestBuildCallOfferVideo(t *testing.T) {
	n := buildCallOffer("ID2", "me@s.whatsapp.net", "5551@s.whatsapp.net", "CALL2", true)
	offer, ok := childByTag(n, "offer")
	if !ok {
		t.Fatal("missing <offer>")
	}
	if _, ok := childByTag(offer, "video"); !ok {
		t.Fatal("video offer must carry <video>")
	}
	if _, ok := childByTag(offer, "audio"); !ok {
		t.Fatal("video offer must still carry <audio>")
	}
}

func TestBuildCallTerminate(t *testing.T) {
	n := buildCallTerminate("me@s.whatsapp.net", "CALL3", "5551@s.whatsapp.net")
	if n.Tag != "call" {
		t.Fatalf("tag = %q", n.Tag)
	}
	if n.Attrs["from"] != "me@s.whatsapp.net" || n.Attrs["to"] != "5551@s.whatsapp.net" {
		t.Fatalf("call attrs wrong: %+v", n.Attrs)
	}
	term, ok := childByTag(n, "terminate")
	if !ok {
		t.Fatal("missing <terminate>")
	}
	if term.Attrs["call-id"] != "CALL3" || term.Attrs["count"] != "0" {
		t.Fatalf("terminate attrs wrong: %+v", term.Attrs)
	}
}
