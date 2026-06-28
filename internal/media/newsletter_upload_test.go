package media

import "testing"

func TestNewsletterUploadPath(t *testing.T) {
	for mt, want := range map[MediaType]string{
		Image:    "/newsletter/newsletter-image",
		Video:    "/newsletter/newsletter-video",
		Audio:    "/newsletter/newsletter-audio",
		Document: "/newsletter/newsletter-document",
	} {
		got, err := newsletterUploadPath(mt)
		if err != nil {
			t.Errorf("newsletterUploadPath(%v) error: %v", mt, err)
			continue
		}
		if got != want {
			t.Errorf("newsletterUploadPath(%v) = %q, want %q", mt, got, want)
		}
	}
	// Types without a channel upload path must error (no encrypted /mms/ fallback).
	for _, mt := range []MediaType{History, AppState} {
		if _, err := newsletterUploadPath(mt); err == nil {
			t.Errorf("newsletterUploadPath(%v) should error", mt)
		}
	}
}
