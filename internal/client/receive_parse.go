package client

import "github.com/felipeleal/wa-go/internal/waproto"

// This file turns a decoded (decrypted + unpadded) WAProto.Message into a rich
// MessageEvent. It mirrors the content oneof Baileys handles in
// Utils/messages.ts / process-message.ts: it unwraps the device/ephemeral/
// view-once wrappers, then dispatches on the concrete content field.

// unwrapMessage peels the transport wrappers WhatsApp nests around the real
// content: deviceSentMessage (a copy of a message we sent from another device)
// and the FutureProofMessage wrappers (ephemeralMessage / viewOnceMessage /
// viewOnceMessageV2 / documentWithCaptionMessage). It recurses until it reaches
// a non-wrapper message and reports, via the returned flags, whether the content
// was view-once and/or ephemeral so the caller can mark the event.
func unwrapMessageFlags(m *waproto.Message) (inner *waproto.Message, viewOnce, ephemeral bool) {
	for m != nil {
		switch {
		case m.GetDeviceSentMessage().GetMessage() != nil:
			m = m.GetDeviceSentMessage().GetMessage()
		case m.GetViewOnceMessage().GetMessage() != nil:
			viewOnce = true
			m = m.GetViewOnceMessage().GetMessage()
		case m.GetViewOnceMessageV2().GetMessage() != nil:
			viewOnce = true
			m = m.GetViewOnceMessageV2().GetMessage()
		case m.GetViewOnceMessageV2Extension().GetMessage() != nil:
			viewOnce = true
			m = m.GetViewOnceMessageV2Extension().GetMessage()
		case m.GetEphemeralMessage().GetMessage() != nil:
			ephemeral = true
			m = m.GetEphemeralMessage().GetMessage()
		case m.GetDocumentWithCaptionMessage().GetMessage() != nil:
			// documentWithCaptionMessage is a pure container; no flag to surface.
			m = m.GetDocumentWithCaptionMessage().GetMessage()
		default:
			return m, viewOnce, ephemeral
		}
	}
	return m, viewOnce, ephemeral
}

// unwrapMessage peels the transport wrappers and returns the inner content,
// discarding the view-once / ephemeral flags. Kept for callers (receive.go) that
// only need the unwrapped message; parseMessage uses unwrapMessageFlags directly.
func unwrapMessage(m *waproto.Message) *waproto.Message {
	inner, _, _ := unwrapMessageFlags(m)
	return inner
}

// parseMessage builds a MessageEvent from a decoded WAProto.Message. from/id/
// timestamp/pushName/sender/isGroup come from the stanza envelope (filled by the
// caller); this function fills Type and the typed content. Text is populated for
// text bodies and media captions so existing consumers keep working.
func parseMessage(m *waproto.Message) MessageEvent {
	m, viewOnce, ephemeral := unwrapMessageFlags(m)
	ev := MessageEvent{Type: MessageUnknown, Raw: m, ViewOnce: viewOnce, Ephemeral: ephemeral}
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

	case m.GetPtvMessage() != nil:
		// PTV (round video note) reuses VideoMessage; surface it as video media so
		// it can be downloaded/decrypted the same way.
		pv := m.GetPtvMessage()
		ev.Type = MessageVideo
		ev.Text = pv.GetCaption()
		ev.Media = &MediaInfo{
			Kind:          MediaVideo,
			Mimetype:      pv.GetMimetype(),
			Caption:       pv.GetCaption(),
			FileLength:    pv.GetFileLength(),
			MediaKey:      pv.GetMediaKey(),
			DirectPath:    pv.GetDirectPath(),
			URL:           pv.GetUrl(),
			FileSha256:    pv.GetFileSha256(),
			FileEncSha256: pv.GetFileEncSha256(),
			Width:         pv.GetWidth(),
			Height:        pv.GetHeight(),
			Seconds:       pv.GetSeconds(),
			Thumbnail:     pv.GetJpegThumbnail(),
		}
		applyContext(&ev, pv.GetContextInfo())

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

	case m.GetPollUpdateMessage() != nil:
		pu := m.GetPollUpdateMessage()
		ev.Type = MessagePollVote
		ev.PollVote = &PollVoteInfo{
			PollKey:    messageRefFromKey(pu.GetPollCreationMessageKey()),
			EncPayload: pu.GetVote().GetEncPayload(),
			EncIV:      pu.GetVote().GetEncIv(),
		}

	case m.GetButtonsMessage() != nil:
		bm := m.GetButtonsMessage()
		ev.Type = MessageButtons
		ev.Text = bm.GetContentText()
		for _, b := range bm.GetButtons() {
			ev.Buttons = append(ev.Buttons, ButtonInfo{
				ID:   b.GetButtonId(),
				Text: b.GetButtonText().GetDisplayText(),
			})
		}
		applyContext(&ev, bm.GetContextInfo())

	case m.GetListMessage() != nil:
		lm := m.GetListMessage()
		ev.Type = MessageList
		ev.Text = lm.GetDescription()
		li := &ListInfo{
			Title:       lm.GetTitle(),
			Description: lm.GetDescription(),
			ButtonText:  lm.GetButtonText(),
			FooterText:  lm.GetFooterText(),
		}
		for _, s := range lm.GetSections() {
			sec := ListItemSection{Title: s.GetTitle()}
			for _, r := range s.GetRows() {
				sec.Rows = append(sec.Rows, ListItemRow{
					RowID:       r.GetRowId(),
					Title:       r.GetTitle(),
					Description: r.GetDescription(),
				})
			}
			li.Sections = append(li.Sections, sec)
		}
		ev.List = li
		applyContext(&ev, lm.GetContextInfo())

	case m.GetTemplateMessage() != nil:
		tm := m.GetTemplateMessage()
		ev.Type = MessageTemplate
		if h := tm.GetHydratedTemplate(); h != nil {
			ev.Text = h.GetHydratedContentText()
			for _, b := range h.GetHydratedButtons() {
				if qr := b.GetQuickReplyButton(); qr != nil {
					ev.Buttons = append(ev.Buttons, ButtonInfo{
						ID:   qr.GetId(),
						Text: qr.GetDisplayText(),
					})
				} else if u := b.GetUrlButton(); u != nil {
					ev.Buttons = append(ev.Buttons, ButtonInfo{
						ID:   u.GetUrl(),
						Text: u.GetDisplayText(),
					})
				}
			}
		}
		applyContext(&ev, tm.GetContextInfo())

	case m.GetInteractiveMessage() != nil:
		im := m.GetInteractiveMessage()
		ev.Type = MessageInteractive
		ev.Text = im.GetBody().GetText()
		applyContext(&ev, im.GetContextInfo())

	case m.GetButtonsResponseMessage() != nil:
		br := m.GetButtonsResponseMessage()
		ev.Type = MessageButtonReply
		ev.ButtonReply = &ButtonReplyInfo{
			SelectedID: br.GetSelectedButtonId(),
			Text:       br.GetSelectedDisplayText(),
		}
		applyContext(&ev, br.GetContextInfo())

	case m.GetListResponseMessage() != nil:
		lr := m.GetListResponseMessage()
		ev.Type = MessageListReply
		ev.ListReply = &ListReplyInfo{
			RowID:       lr.GetSingleSelectReply().GetSelectedRowId(),
			Title:       lr.GetTitle(),
			Description: lr.GetDescription(),
		}
		ev.Text = lr.GetTitle()
		applyContext(&ev, lr.GetContextInfo())

	case m.GetTemplateButtonReplyMessage() != nil:
		tr := m.GetTemplateButtonReplyMessage()
		ev.Type = MessageTemplateReply
		ev.ButtonReply = &ButtonReplyInfo{
			SelectedID: tr.GetSelectedId(),
			Text:       tr.GetSelectedDisplayText(),
		}
		ev.Text = tr.GetSelectedDisplayText()
		applyContext(&ev, tr.GetContextInfo())

	case m.GetInteractiveResponseMessage() != nil:
		ir := m.GetInteractiveResponseMessage()
		ev.Type = MessageInteractiveReply
		ev.InteractiveReply = &InteractiveReplyInfo{
			Text:       ir.GetBody().GetText(),
			Name:       ir.GetNativeFlowResponseMessage().GetName(),
			ParamsJSON: ir.GetNativeFlowResponseMessage().GetParamsJson(),
		}
		ev.Text = ir.GetBody().GetText()
		applyContext(&ev, ir.GetContextInfo())

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
