package client

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/jfelipesjc/wa-go/internal/media"
	"github.com/jfelipesjc/wa-go/internal/waproto"
	"google.golang.org/protobuf/proto"
)

// History sync ingestion.
//
// After login the server pushes one or more HISTORY_SYNC_NOTIFICATION messages
// (inside a ProtocolMessage). Each notification references an encrypted, zlib-
// compressed HistorySync blob to download via the media transfer using the
// "md-msg-hist" media type (HKDF info "WhatsApp History Keys"). Once downloaded
// and inflated it is a waproto.HistorySync carrying the conversations + messages.
//
// The crypto/network download is live (needs a media_conn), so it is kept in
// handleHistorySync; the pure decode (inflate + unmarshal) lives in
// DecodeHistorySync so it is unit-testable offline.

// maxHistorySyncSize caps the inflated HistorySync to guard against a malicious
// or corrupt blob inflating without bound (zip-bomb protection). Real history
// chunks are well under this; Baileys streams them but the chunks are bounded by
// the server's storageQuotaMb config.
const maxHistorySyncSize = 256 << 20 // 256 MiB

// DecodeHistorySync inflates a zlib-compressed HistorySync blob and unmarshals it
// into a waproto.HistorySync. The input is the *decrypted* media payload (the
// output of the media transfer), which WhatsApp zlib-compresses before
// encryption. It is deliberately free of any network/crypto so it can be tested
// offline.
func DecodeHistorySync(compressed []byte) (*waproto.HistorySync, error) {
	if len(compressed) == 0 {
		return nil, errors.New("client: empty history sync blob")
	}
	zr, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("client: history sync zlib: %w", err)
	}
	defer zr.Close()

	raw, err := io.ReadAll(io.LimitReader(zr, maxHistorySyncSize+1))
	if err != nil {
		return nil, fmt.Errorf("client: history sync inflate: %w", err)
	}
	if len(raw) > maxHistorySyncSize {
		return nil, fmt.Errorf("client: history sync exceeds %d bytes", maxHistorySyncSize)
	}

	var hs waproto.HistorySync
	if err := proto.Unmarshal(raw, &hs); err != nil {
		return nil, fmt.Errorf("client: unmarshal history sync: %w", err)
	}
	return &hs, nil
}

// handleHistorySync downloads the blob referenced by a HistorySyncNotification,
// decodes it, and emits a HistorySyncEvent. The download is live (it resolves a
// media_conn and GETs the encrypted blob); tests exercise DecodeHistorySync
// directly rather than driving a real download.
func (c *Client) handleHistorySync(ctx context.Context, n *waproto.HistorySyncNotification) error {
	return c.handleHistorySyncWithFetcher(ctx, http.DefaultClient, n)
}

// handleHistorySyncWithFetcher is handleHistorySync with an injectable HTTP
// transport (used by tests / callers that need a custom client).
func (c *Client) handleHistorySyncWithFetcher(ctx context.Context, f media.Fetcher, n *waproto.HistorySyncNotification) error {
	if n == nil {
		return errors.New("client: nil history sync notification")
	}
	if f == nil {
		f = http.DefaultClient
	}

	directPath := n.GetDirectPath()
	mediaKey := n.GetMediaKey()
	if directPath == "" || len(mediaKey) == 0 {
		return errors.New("client: history sync notification missing directPath/mediaKey")
	}

	conn, err := c.mediaConn(ctx)
	if err != nil {
		return err
	}

	compressed, err := media.Download(ctx, f, directPath, mediaKey, media.History, conn.Hosts, conn.Auth)
	if err != nil {
		return fmt.Errorf("client: download history sync: %w", err)
	}

	hs, err := DecodeHistorySync(compressed)
	if err != nil {
		return err
	}

	c.emitHistorySync(hs)
	return nil
}

// emitHistorySync builds and emits a HistorySyncEvent from a decoded HistorySync.
func (c *Client) emitHistorySync(hs *waproto.HistorySync) {
	if hs == nil {
		return
	}
	c.emit(HistorySyncEvent{
		SyncType:      hs.GetSyncType(),
		Progress:      hs.GetProgress(),
		ChunkOrder:    hs.GetChunkOrder(),
		Conversations: hs.GetConversations(),
		Pushnames:     hs.GetPushnames(),
		Raw:           hs,
	})
}
