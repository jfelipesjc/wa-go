// Package client: profile.go implements the WhatsApp profile IQs — display
// name, status (about) text, and profile picture (set / remove / fetch URL /
// fetch status). These mirror Baileys' Socket/chats.js helpers
// (updateProfileStatus, updateProfilePicture, removeProfilePicture,
// profilePictureUrl, fetchStatus) using plain request iqs.
//
// All stanzas go to sWhatsAppNet. The iq builders are pure functions so their
// structure/attributes can be asserted offline; the public methods wire them
// through c.sendIQ on the live session and parse the reply.
//
// Confirmed Baileys structures (lib/Socket/chats.js):
//
//	updateProfileStatus:
//	  <iq to=@s.whatsapp.net type=set xmlns=status>
//	    <status>...utf8 bytes...</status>
//	  </iq>
//
//	updateProfilePicture(jid, img):
//	  <iq to=@s.whatsapp.net type=set xmlns=w:profile:picture [target=jid]>
//	    <picture type=image>...jpeg bytes...</picture>
//	  </iq>
//	(target attr is present only when updating someone/some-group other than
//	 self; for self it is omitted.)
//
//	removeProfilePicture(jid):
//	  <iq to=@s.whatsapp.net type=set xmlns=w:profile:picture [target=jid]/>
//
//	profilePictureUrl(jid, type):
//	  <iq target=jid to=@s.whatsapp.net type=get xmlns=w:profile:picture>
//	    <picture type=preview|image query=url/>
//	  </iq>
//	reply: <iq ...><picture url=... .../></iq>
//
// updateProfileName goes through app-state exactly like Baileys' chatModify
// (pushNameSetting): a SET mutation in collection critical_block at index
// ["setting_pushName"], apiVersion 1. This persists the name on the account and
// syncs to the primary phone (a transient presence push name does not).
//
// fetchStatus in Baileys uses a USync status query. Here FetchStatus uses the
// simpler per-jid status iq form the server also accepts:
//
//	<iq to=<jid> type=get xmlns=status><status/></iq>
//
// reply: <iq ...><status>...text...</status></iq>
package client

import (
	"context"
	"errors"

	"github.com/jfelipesjc/wa-go/internal/wire"
)

// updateProfileStatusNode builds the set-status iq:
//
//	<iq to=@s.whatsapp.net type=set xmlns=status id=...>
//	  <status>status</status>
//	</iq>
func updateProfileStatusNode(id, status string) wire.Node {
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    sWhatsAppNet,
			"type":  "set",
			"xmlns": "status",
			"id":    id,
		},
		Content: []wire.Node{
			{Tag: "status", Content: []byte(status)},
		},
	}
}

// setProfilePictureNode builds the set-picture iq. When target is non-empty it
// is included as the target attr (updating another user/group); for self it is
// omitted.
//
//	<iq to=@s.whatsapp.net type=set xmlns=w:profile:picture [target=...] id=...>
//	  <picture type=image>jpeg</picture>
//	</iq>
func setProfilePictureNode(id, target string, jpeg []byte) wire.Node {
	attrs := map[string]string{
		"to":    sWhatsAppNet,
		"type":  "set",
		"xmlns": "w:profile:picture",
		"id":    id,
	}
	if target != "" {
		attrs["target"] = target
	}
	return wire.Node{
		Tag:   "iq",
		Attrs: attrs,
		Content: []wire.Node{
			{Tag: "picture", Attrs: map[string]string{"type": "image"}, Content: jpeg},
		},
	}
}

// removeProfilePictureNode builds the remove-picture iq (no children):
//
//	<iq to=@s.whatsapp.net type=set xmlns=w:profile:picture [target=...] id=.../>
func removeProfilePictureNode(id, target string) wire.Node {
	attrs := map[string]string{
		"to":    sWhatsAppNet,
		"type":  "set",
		"xmlns": "w:profile:picture",
		"id":    id,
	}
	if target != "" {
		attrs["target"] = target
	}
	return wire.Node{Tag: "iq", Attrs: attrs}
}

// profilePictureURLNode builds the get-picture-url iq:
//
//	<iq target=jid to=@s.whatsapp.net type=get xmlns=w:profile:picture id=...>
//	  <picture type=preview|image query=url/>
//	</iq>
func profilePictureURLNode(id, jid string, preview bool) wire.Node {
	picType := "image"
	if preview {
		picType = "preview"
	}
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"target": jid,
			"to":     sWhatsAppNet,
			"type":   "get",
			"xmlns":  "w:profile:picture",
			"id":     id,
		},
		Content: []wire.Node{
			{Tag: "picture", Attrs: map[string]string{"type": picType, "query": "url"}},
		},
	}
}

// fetchStatusNode builds the get-status iq for a jid:
//
//	<iq to=<jid> type=get xmlns=status id=...><status/></iq>
func fetchStatusNode(id, jid string) wire.Node {
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    jid,
			"type":  "get",
			"xmlns": "status",
			"id":    id,
		},
		Content: []wire.Node{{Tag: "status"}},
	}
}

// parseProfilePictureURL extracts the url attribute from a get-picture-url
// reply: <iq ...><picture url=.../></iq>. Returns "" if absent.
func parseProfilePictureURL(reply wire.Node) string {
	pic, ok := childByTag(reply, "picture")
	if !ok {
		return ""
	}
	return pic.Attrs["url"]
}

// parseFetchStatus extracts the status text from a get-status reply:
// <iq ...><status>text</status></iq>. Returns "" if absent.
func parseFetchStatus(reply wire.Node) string {
	st, ok := childByTag(reply, "status")
	if !ok {
		return ""
	}
	return string(nodeBytes(st))
}

// presenceNameNode builds an available-presence stanza carrying the push name:
//
//	<presence type=available [from=me] name=name/>
func presenceNameNode(me, name string) wire.Node {
	attrs := map[string]string{"type": string(PresenceAvailable), "name": name}
	if me != "" {
		attrs["from"] = me
	}
	return wire.Node{Tag: "presence", Attrs: attrs}
}

// UpdateProfileName advertises the device's display (push) name via an
// available-presence stanza — the safe, non-destructive path. It is also
// persisted to the local creds so the UI reflects it.
//
// NOTE: the app-state `pushNameSetting` mutation (collection critical_block) that
// the official web client uses is NOT used here: our current app-state encoding
// for that high-security collection is rejected by the server and triggers a
// device unlink (conflict:device_removed). Until that path is implemented and
// validated correctly, we keep the presence-only behaviour, which never unlinks
// the session. The push name propagates to contacts but does not rewrite the
// primary phone's profile name.
func (c *Client) UpdateProfileName(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("client: UpdateProfileName requires a non-empty name")
	}
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: UpdateProfileName requires a live session")
	}
	if err := sess.send(presenceNameNode(sess.creds.Me, name)); err != nil {
		return err
	}
	if creds, ok, err := c.store.LoadCreds(); err == nil && ok && creds != nil {
		creds.PushName = name
		_ = c.store.SaveCreds(creds)
	}
	sess.creds.PushName = name
	return nil
}

// UpdateProfileStatus sets the account's status (about) text.
func (c *Client) UpdateProfileStatus(ctx context.Context, status string) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: UpdateProfileStatus requires a live session")
	}
	req := updateProfileStatusNode(c.nextIQID("status"), status)
	_, err := c.sendIQ(ctx, sess, req)
	return err
}

// SetProfilePicture sets the JPEG profile picture for the given jid. Pass an
// empty jid to update your own picture.
func (c *Client) SetProfilePicture(ctx context.Context, jid string, jpegBytes []byte) error {
	if len(jpegBytes) == 0 {
		return errors.New("client: SetProfilePicture requires image bytes")
	}
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: SetProfilePicture requires a live session")
	}
	req := setProfilePictureNode(c.nextIQID("picture"), profilePicTarget(sess, jid), jpegBytes)
	_, err := c.sendIQ(ctx, sess, req)
	return err
}

// RemoveProfilePicture removes the profile picture for the given jid. Pass an
// empty jid to remove your own picture.
func (c *Client) RemoveProfilePicture(ctx context.Context, jid string) error {
	sess, ok := c.activeSession()
	if !ok {
		return errors.New("client: RemoveProfilePicture requires a live session")
	}
	req := removeProfilePictureNode(c.nextIQID("picture"), profilePicTarget(sess, jid))
	_, err := c.sendIQ(ctx, sess, req)
	return err
}

// ProfilePictureURL fetches the URL of the profile picture for jid. preview=true
// returns the low-res thumbnail URL, false the full-res image URL. An empty
// string (with nil error) means no picture is set.
func (c *Client) ProfilePictureURL(ctx context.Context, jid string, preview bool) (string, error) {
	if jid == "" {
		return "", errors.New("client: ProfilePictureURL requires a jid")
	}
	sess, ok := c.activeSession()
	if !ok {
		return "", errors.New("client: ProfilePictureURL requires a live session")
	}
	reply, err := c.sendIQ(ctx, sess, profilePictureURLNode(c.nextIQID("picture"), jid, preview))
	if err != nil {
		return "", err
	}
	return parseProfilePictureURL(reply), nil
}

// FetchStatus fetches the status (about) text for jid.
func (c *Client) FetchStatus(ctx context.Context, jid string) (string, error) {
	if jid == "" {
		return "", errors.New("client: FetchStatus requires a jid")
	}
	sess, ok := c.activeSession()
	if !ok {
		return "", errors.New("client: FetchStatus requires a live session")
	}
	// Modern path: usync STATUS protocol. The old <iq xmlns=status> is deprecated
	// and gets no reply (timed out).
	reply, err := c.sendIQ(ctx, sess, usyncStatusQueryNode(c.nextIQID("wa-go-usync-"), c.nextIQID(""), []string{jid}))
	if err != nil {
		return "", err
	}
	return parseUSyncStatus(reply), nil
}

// profilePicTarget returns the target attr for a picture iq: empty when jid is
// empty or equals our own user jid (self update, where Baileys omits target),
// otherwise jid.
func profilePicTarget(sess *session, jid string) string {
	if jid == "" {
		return ""
	}
	if sess != nil && sess.creds != nil && jid == sess.creds.Me {
		return ""
	}
	return jid
}
