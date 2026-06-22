package client

import "github.com/felipeleal/wa-go/internal/waproto"

// This file turns a decoded (decrypted + unpadded) WAProto.Message into a rich
// MessageEvent. It mirrors the content oneof Baileys handles in
// Utils/messages.ts / process-message.ts: it unwraps the device/ephemeral/
// view-once wrappers, then dispatches on the concrete content field.

// unwrapMessage peels the transport wrappers WhatsApp nests around the real
// content: deviceSentMessage (a copy of a message we sent from another device)
// and, when the schema carries them, ephemeralMessage / viewOnceMessage /
// futureProofMessage. It recurses until it reaches a non-wrapper message. The
// returned bool reports whether any unwrapping happened (unused today but keeps
// the contract explicit).
func unwrapMessage(m *waproto.Message) *waproto.Message {
	for m != nil {
		if ds := m.GetDeviceSentMessage(); ds != nil && ds.GetMessage() != nil {
			m = ds.GetMessage()
			continue
		}
		// ephemeralMessage / viewOnceMessage / futureProofMessage are optional in
		// the schema; only unwrap when the generated type exposes them. They are
		// added defensively below via the helper getters when present.
		if inner := ephemeralInner(m); inner != nil {
			m = inner
			continue
		}
		break
	}
	return m
}

// ephemeralInner returns the inner message of an ephemeral / view-once wrapper,
// or nil when neither is present. The current generated schema does not include
// ephemeralMessage / viewOnceMessage wrapper fields, so this is a no-op today; it
// exists so unwrapMessage stays correct (and one place changes) if those wrapper
// types are added to the protobuf later.
func ephemeralInner(m *waproto.Message) *waproto.Message {
	_ = m
	return nil
}

// parseMessage builds a MessageEvent from a decoded WAProto.Message. from/id/
// timestamp/pushName/sender/isGroup come from the stanza envelope (filled by the
// caller); this function fills Type and the typed content. Text is populated for
// text bodies and media captions so existing consumers keep working.
func parseMessage(m *waproto.Message) MessageEvent {
	m = unwrapMessage(m)
	ev := MessageEvent{Type: MessageUnknown, Raw: m}
	if m == nil {
		return ev
	}

	switch {
	case m.GetConversation() != "":
		ev.Type = MessageText
		ev.Text = m.GetConversation()

	case m.GetExtendedTextMessage() != nil:
		et := m.GetExtendedTextMessage()
		ev.Type = MessageText
		ev.Text = et.GetText()
		applyContext(&ev, et.GetContextInfo())

	case m.GetImageMessage() != nil:
		im := m.GetImageMessage()
		ev.Type = MessageImage
		ev.Text = im.GetCaption()
		ev.Media = &MediaInfo{
			Kind:          MediaImage,
			Mimetype:      im.GetMimetype(),
			Caption:       im.GetCaption(),
			FileLength:    im.GetFileLength(),
			MediaKey:      im.GetMediaKey(),
			DirectPath:    im.GetDirectPath(),
			URL:           im.GetUrl(),
			FileSha256:    im.GetFileSha256(),
			FileEncSha256: im.GetFileEncSha256(),
			Width:         im.GetWidth(),
			Height:        im.GetHeight(),
			Thumbnail:     im.GetJpegThumbnail(),
		}
		applyContext(&ev, im.GetContextInfo())

	case m.GetVideoMessage() != nil:
		vm := m.GetVideoMessage()
		ev.Type = MessageVideo
		ev.Text = vm.GetCaption()
		ev.Media = &MediaInfo{
			Kind:          MediaVideo,
			Mimetype:      vm.GetMimetype(),
			Caption:       vm.GetCaption(),
			FileLength:    vm.GetFileLength(),
			MediaKey:      vm.GetMediaKey(),
			DirectPath:    vm.GetDirectPath(),
			URL:           vm.GetUrl(),
			FileSha256:    vm.GetFileSha256(),
			FileEncSha256: vm.GetFileEncSha256(),
			Width:         vm.GetWidth(),
			Height:        vm.GetHeight(),
			Seconds:       vm.GetSeconds(),
			Thumbnail:     vm.GetJpegThumbnail(),
		}
		applyContext(&ev, vm.GetContextInfo())

	case m.GetAudioMessage() != nil:
		am := m.GetAudioMessage()
		ev.Type = MessageAudio
		ev.Media = &MediaInfo{
			Kind:          MediaAudio,
			Mimetype:      am.GetMimetype(),
			FileLength:    am.GetFileLength(),
			MediaKey:      am.GetMediaKey(),
			DirectPath:    am.GetDirectPath(),
			URL:           am.GetUrl(),
			FileSha256:    am.GetFileSha256(),
			FileEncSha256: am.GetFileEncSha256(),
			Seconds:       am.GetSeconds(),
			IsPTT:         am.GetPtt(),
		}
		applyContext(&ev, am.GetContextInfo())

	case m.GetDocumentMessage() != nil:
		dm := m.GetDocumentMessage()
		ev.Type = MessageDocument
		ev.Text = dm.GetCaption()
		ev.Media = &MediaInfo{
			Kind:          MediaDocument,
			Mimetype:      dm.GetMimetype(),
			Caption:       dm.GetCaption(),
			FileName:      dm.GetFileName(),
			FileLength:    dm.GetFileLength(),
			MediaKey:      dm.GetMediaKey(),
			DirectPath:    dm.GetDirectPath(),
			URL:           dm.GetUrl(),
			FileSha256:    dm.GetFileSha256(),
			FileEncSha256: dm.GetFileEncSha256(),
			PageCount:     dm.GetPageCount(),
			Thumbnail:     dm.GetJpegThumbnail(),
		}
		applyContext(&ev, dm.GetContextInfo())

	case m.GetStickerMessage() != nil:
		sm := m.GetStickerMessage()
		ev.Type = MessageSticker
		ev.Media = &MediaInfo{
			Kind:          MediaSticker,
			Mimetype:      sm.GetMimetype(),
			FileLength:    sm.GetFileLength(),
			MediaKey:      sm.GetMediaKey(),
			DirectPath:    sm.GetDirectPath(),
			URL:           sm.GetUrl(),
			FileSha256:    sm.GetFileSha256(),
			FileEncSha256: sm.GetFileEncSha256(),
			Width:         sm.GetWidth(),
			Height:        sm.GetHeight(),
			IsAnimated:    sm.GetIsAnimated(),
			Thumbnail:     sm.GetPngThumbnail(),
		}
		applyContext(&ev, sm.GetContextInfo())

	case m.GetLocationMessage() != nil:
		lm := m.GetLocationMessage()
		ev.Type = MessageLocation
		ev.Location = &LocationInfo{
			Latitude:  lm.GetDegreesLatitude(),
			Longitude: lm.GetDegreesLongitude(),
			Name:      lm.GetName(),
			Address:   lm.GetAddress(),
			IsLive:    lm.GetIsLive(),
		}
		applyContext(&ev, lm.GetContextInfo())

	case m.GetLiveLocationMessage() != nil:
		llm := m.GetLiveLocationMessage()
		ev.Type = MessageLocation
		ev.Location = &LocationInfo{
			Latitude:  llm.GetDegreesLatitude(),
			Longitude: llm.GetDegreesLongitude(),
			IsLive:    true,
		}
		applyContext(&ev, llm.GetContextInfo())

	case m.GetContactMessage() != nil:
		cm := m.GetContactMessage()
		ev.Type = MessageContact
		ev.Contact = &ContactInfo{
			DisplayName: cm.GetDisplayName(),
			Vcard:       cm.GetVcard(),
		}
		applyContext(&ev, cm.GetContextInfo())

	case m.GetReactionMessage() != nil:
		rm := m.GetReactionMessage()
		ev.Type = MessageReaction
		ev.Reaction = &ReactionInfo{
			Key:  messageRefFromKey(rm.GetKey()),
			Text: rm.GetText(),
		}

	case m.GetPollCreationMessage() != nil:
		pm := m.GetPollCreationMessage()
		ev.Type = MessagePoll
		opts := make([]string, 0, len(pm.GetOptions()))
		for _, o := range pm.GetOptions() {
			opts = append(opts, o.GetOptionName())
		}
		ev.Poll = &PollInfo{
			Name:                   pm.GetName(),
			Options:                opts,
			SelectableOptionsCount: pm.GetSelectableOptionsCount(),
		}
		applyContext(&ev, pm.GetContextInfo())

	case m.GetProtocolMessage() != nil:
		applyProtocol(&ev, m.GetProtocolMessage())
	}

	return ev
}

// applyProtocol handles protocolMessage: REVOKE (delete) and MESSAGE_EDIT. For
// an edit the embedded editedMessage is parsed into the same event so the new
// content (text/media) is available, with Edited pointing at the original key.
func applyProtocol(ev *MessageEvent, pm *waproto.ProtocolMessage) {
	switch pm.GetType() {
	case waproto.ProtocolMessage_REVOKE:
		ev.Type = MessageRevoke
		ref := messageRefFromKey(pm.GetKey())
		ev.Revoked = &ref
	case waproto.ProtocolMessage_MESSAGE_EDIT:
		ref := messageRefFromKey(pm.GetKey())
		if edited := pm.GetEditedMessage(); edited != nil {
			inner := parseMessage(edited)
			// Carry over the decoded content but mark it as an edit.
			ev.Text = inner.Text
			ev.Media = inner.Media
			ev.Location = inner.Location
			ev.Contact = inner.Contact
			ev.Poll = inner.Poll
			ev.Quoted = inner.Quoted
			ev.Mentions = inner.Mentions
		}
		ev.Type = MessageEdit
		ev.Edited = &ref
	default:
		// Other protocol messages (ephemeral settings, app-state key share, ...)
		// are not surfaced as message events here.
		ev.Type = MessageUnknown
	}
}

// applyContext copies reply (quoted) and mention metadata from a ContextInfo
// onto the event.
func applyContext(ev *MessageEvent, ci *waproto.ContextInfo) {
	if ci == nil {
		return
	}
	if mentions := ci.GetMentionedJid(); len(mentions) > 0 {
		ev.Mentions = append([]string(nil), mentions...)
	}
	if ci.GetStanzaId() != "" || ci.GetQuotedMessage() != nil {
		q := &QuotedInfo{
			StanzaID:    ci.GetStanzaId(),
			Participant: ci.GetParticipant(),
			Message:     ci.GetQuotedMessage(),
		}
		if qm := ci.GetQuotedMessage(); qm != nil {
			q.Text = parseMessage(qm).Text
		}
		ev.Quoted = q
	}
}

// messageRefFromKey converts a WAProto.MessageKey to a MessageRef.
func messageRefFromKey(k *waproto.MessageKey) MessageRef {
	if k == nil {
		return MessageRef{}
	}
	return MessageRef{
		ID:          k.GetId(),
		FromMe:      k.GetFromMe(),
		RemoteJID:   k.GetRemoteJid(),
		Participant: k.GetParticipant(),
	}
}
