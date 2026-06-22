// Package wire implements the WhatsApp binary wire protocol.
package wire

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
)

// ──────────────────────────────────────────────────────────────────────────────
// Decoder
// ──────────────────────────────────────────────────────────────────────────────

// DecodeNode decodes a raw (transport-stripped, uncompressed) binary node.
// b must be the plain node bytes with no 0x00/0x02 prefix.
func DecodeNode(b []byte) (Node, error) {
	d := &decoder{buf: b, pos: 0}
	node, err := d.readNode()
	if err != nil {
		return Node{}, err
	}
	return node, nil
}

type decoder struct {
	buf []byte
	pos int
}

func (d *decoder) checkEOS(n int) error {
	if d.pos+n > len(d.buf) {
		return fmt.Errorf("wire: end of stream (pos=%d need=%d len=%d)", d.pos, n, len(d.buf))
	}
	return nil
}

func (d *decoder) readByte() (byte, error) {
	if err := d.checkEOS(1); err != nil {
		return 0, err
	}
	b := d.buf[d.pos]
	d.pos++
	return b, nil
}

func (d *decoder) readBytes(n int) ([]byte, error) {
	if err := d.checkEOS(n); err != nil {
		return nil, err
	}
	b := d.buf[d.pos : d.pos+n]
	d.pos += n
	return b, nil
}

// readInt reads n bytes big-endian and returns an int.
func (d *decoder) readInt(n int) (int, error) {
	if err := d.checkEOS(n); err != nil {
		return 0, err
	}
	val := 0
	for i := 0; i < n; i++ {
		val = (val << 8) | int(d.buf[d.pos+i])
	}
	d.pos += n
	return val, nil
}

// readInt20 reads the WA 20-bit integer (3 bytes: (b[0]&0xf)<<16 | b[1]<<8 | b[2]).
func (d *decoder) readInt20() (int, error) {
	if err := d.checkEOS(3); err != nil {
		return 0, err
	}
	v := (int(d.buf[d.pos]&0x0f) << 16) | (int(d.buf[d.pos+1]) << 8) | int(d.buf[d.pos+2])
	d.pos += 3
	return v, nil
}

func (d *decoder) isListTag(tag byte) bool {
	return tag == LIST_EMPTY || tag == LIST_8 || tag == LIST_16
}

func (d *decoder) readListSize(tag byte) (int, error) {
	switch tag {
	case LIST_EMPTY:
		return 0, nil
	case LIST_8:
		b, err := d.readByte()
		return int(b), err
	case LIST_16:
		return d.readInt(2)
	default:
		return 0, fmt.Errorf("wire: invalid list tag: %d", tag)
	}
}

// unpackNibble maps a 4-bit nibble to a rune per the WA spec.
func unpackNibble(v byte) (byte, error) {
	if v <= 9 {
		return '0' + v, nil
	}
	switch v {
	case 10:
		return '-', nil
	case 11:
		return '.', nil
	case 15:
		return 0, nil
	}
	return 0, fmt.Errorf("wire: invalid nibble: %d", v)
}

// unpackHex maps a 4-bit nibble to an upper-case hex character.
func unpackHex(v byte) (byte, error) {
	if v < 10 {
		return '0' + v, nil
	}
	if v < 16 {
		return 'A' + v - 10, nil
	}
	return 0, fmt.Errorf("wire: invalid hex nibble: %d", v)
}

func (d *decoder) readPacked8(tag byte) (string, error) {
	startByte, err := d.readByte()
	if err != nil {
		return "", err
	}
	count := int(startByte & 0x7f)
	isOdd := (startByte >> 7) != 0

	result := make([]byte, 0, count*2)
	for i := 0; i < count; i++ {
		b, err := d.readByte()
		if err != nil {
			return "", err
		}
		hi := (b & 0xf0) >> 4
		lo := b & 0x0f

		var c1, c2 byte
		if tag == NIBBLE_8 {
			c1, err = unpackNibble(hi)
			if err != nil {
				return "", err
			}
			c2, err = unpackNibble(lo)
			if err != nil {
				return "", err
			}
		} else { // HEX_8
			c1, err = unpackHex(hi)
			if err != nil {
				return "", err
			}
			c2, err = unpackHex(lo)
			if err != nil {
				return "", err
			}
		}
		result = append(result, c1, c2)
	}
	if isOdd {
		result = result[:len(result)-1]
	}
	return string(result), nil
}

func (d *decoder) readJidPair() (string, error) {
	t1, err := d.readByte()
	if err != nil {
		return "", err
	}
	user, err := d.readString(t1)
	if err != nil {
		return "", err
	}
	t2, err := d.readByte()
	if err != nil {
		return "", err
	}
	server, err := d.readString(t2)
	if err != nil {
		return "", err
	}
	if server == "" {
		return "", fmt.Errorf("wire: invalid jid pair: user=%q server=%q", user, server)
	}
	return user + "@" + server, nil
}

func (d *decoder) readAdJid() (string, error) {
	domainType, err := d.readByte()
	if err != nil {
		return "", err
	}
	device, err := d.readByte()
	if err != nil {
		return "", err
	}
	userTag, err := d.readByte()
	if err != nil {
		return "", err
	}
	user, err := d.readString(userTag)
	if err != nil {
		return "", err
	}

	// Determine server from domain type (WAJIDDomains enum).
	var server string
	switch domainType {
	case 1: // LID
		server = "lid"
	case 128: // HOSTED
		server = "hosted"
	case 129: // HOSTED_LID
		server = "hosted.lid"
	default: // WHATSAPP (0)
		server = "s.whatsapp.net"
	}

	// jidEncode: user + (agent? "_"+agent) + (device? ":"+device) + "@" + server
	// device is always present in AD_JID (read as a byte, but 0 means no device in jidEncode)
	// Looking at jid-utils.js jidEncode: device=0 means !!device is false, so omit it
	if device != 0 {
		return fmt.Sprintf("%s:%d@%s", user, device, server), nil
	}
	return user + "@" + server, nil
}

func (d *decoder) readFbJid() (string, error) {
	userTag, err := d.readByte()
	if err != nil {
		return "", err
	}
	user, err := d.readString(userTag)
	if err != nil {
		return "", err
	}
	device, err := d.readInt(2)
	if err != nil {
		return "", err
	}
	serverTag, err := d.readByte()
	if err != nil {
		return "", err
	}
	server, err := d.readString(serverTag)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%d@%s", user, device, server), nil
}

func (d *decoder) readInteropJid() (string, error) {
	userTag, err := d.readByte()
	if err != nil {
		return "", err
	}
	user, err := d.readString(userTag)
	if err != nil {
		return "", err
	}
	device, err := d.readInt(2)
	if err != nil {
		return "", err
	}
	integrator, err := d.readInt(2)
	if err != nil {
		return "", err
	}
	// Try to read optional server string.
	savedPos := d.pos
	server := "interop"
	if d.pos < len(d.buf) {
		serverTag, err := d.readByte()
		if err == nil {
			s, err2 := d.readString(serverTag)
			if err2 == nil {
				server = s
			} else {
				d.pos = savedPos
			}
		}
	}
	return fmt.Sprintf("%d-%s:%d@%s", integrator, user, device, server), nil
}

func (d *decoder) readString(tag byte) (string, error) {
	// Single-byte token range: 1..235
	if tag >= 1 && int(tag) < len(singleByteTokens) {
		return singleByteTokens[tag], nil
	}

	switch tag {
	case DICTIONARY_0, DICTIONARY_1, DICTIONARY_2, DICTIONARY_3:
		dictIdx := int(tag - DICTIONARY_0)
		b, err := d.readByte()
		if err != nil {
			return "", err
		}
		dict := doubleByteTokens[dictIdx]
		if int(b) >= len(dict) {
			return "", fmt.Errorf("wire: double token out of range dict=%d idx=%d", dictIdx, b)
		}
		return dict[b], nil

	case LIST_EMPTY:
		return "", nil

	case BINARY_8:
		n, err := d.readByte()
		if err != nil {
			return "", err
		}
		return d.readUTF8(int(n))

	case BINARY_20:
		n, err := d.readInt20()
		if err != nil {
			return "", err
		}
		return d.readUTF8(n)

	case BINARY_32:
		n, err := d.readInt(4)
		if err != nil {
			return "", err
		}
		return d.readUTF8(n)

	case JID_PAIR:
		return d.readJidPair()

	case FB_JID:
		return d.readFbJid()

	case INTEROP_JID:
		return d.readInteropJid()

	case AD_JID:
		return d.readAdJid()

	case NIBBLE_8, HEX_8:
		return d.readPacked8(tag)

	default:
		return "", fmt.Errorf("wire: invalid string tag: %d", tag)
	}
}

func (d *decoder) readUTF8(n int) (string, error) {
	b, err := d.readBytes(n)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (d *decoder) readList(tag byte) ([]Node, error) {
	size, err := d.readListSize(tag)
	if err != nil {
		return nil, err
	}
	items := make([]Node, 0, size)
	for i := 0; i < size; i++ {
		node, err := d.readNode()
		if err != nil {
			return nil, fmt.Errorf("wire: list item %d: %w", i, err)
		}
		items = append(items, node)
	}
	return items, nil
}

func (d *decoder) readNode() (Node, error) {
	// Read list tag for the node envelope.
	listTag, err := d.readByte()
	if err != nil {
		return Node{}, err
	}
	listSize, err := d.readListSize(listTag)
	if err != nil {
		return Node{}, err
	}

	// Read the tag (header string).
	headerTag, err := d.readByte()
	if err != nil {
		return Node{}, err
	}
	tag, err := d.readString(headerTag)
	if err != nil {
		return Node{}, err
	}

	if listSize == 0 || tag == "" {
		return Node{}, fmt.Errorf("wire: invalid node: listSize=%d tag=%q", listSize, tag)
	}

	attrs := make(map[string]string)

	// Attributes occupy (listSize-1)/2 pairs.
	attrCount := (listSize - 1) >> 1
	for i := 0; i < attrCount; i++ {
		keyTag, err := d.readByte()
		if err != nil {
			return Node{}, fmt.Errorf("wire: attr key %d: %w", i, err)
		}
		key, err := d.readString(keyTag)
		if err != nil {
			return Node{}, fmt.Errorf("wire: attr key %d: %w", i, err)
		}
		valTag, err := d.readByte()
		if err != nil {
			return Node{}, fmt.Errorf("wire: attr val %d: %w", i, err)
		}
		val, err := d.readString(valTag)
		if err != nil {
			return Node{}, fmt.Errorf("wire: attr val %d: %w", i, err)
		}
		attrs[key] = val
	}

	// If listSize is even, there is content.
	var content any
	if listSize%2 == 0 {
		contentTag, err := d.readByte()
		if err != nil {
			return Node{}, fmt.Errorf("wire: content tag: %w", err)
		}
		if d.isListTag(contentTag) {
			children, err := d.readList(contentTag)
			if err != nil {
				return Node{}, fmt.Errorf("wire: content list: %w", err)
			}
			content = children
		} else {
			switch contentTag {
			case BINARY_8:
				n, err := d.readByte()
				if err != nil {
					return Node{}, err
				}
				b, err := d.readBytes(int(n))
				if err != nil {
					return Node{}, err
				}
				// Use make to guarantee a non-nil slice even when n==0.
				// append(nil, emptySlice...) would return nil, losing the
				// distinction between "no content" and "zero-byte content".
				dst := make([]byte, len(b))
				copy(dst, b)
				content = dst
			case BINARY_20:
				n, err := d.readInt20()
				if err != nil {
					return Node{}, err
				}
				b, err := d.readBytes(n)
				if err != nil {
					return Node{}, err
				}
				dst := make([]byte, len(b))
				copy(dst, b)
				content = dst
			case BINARY_32:
				n, err := d.readInt(4)
				if err != nil {
					return Node{}, err
				}
				b, err := d.readBytes(n)
				if err != nil {
					return Node{}, err
				}
				dst := make([]byte, len(b))
				copy(dst, b)
				content = dst
			default:
				// String-typed content.
				s, err := d.readString(contentTag)
				if err != nil {
					return Node{}, fmt.Errorf("wire: content string: %w", err)
				}
				content = s
			}
		}
	}

	return Node{Tag: tag, Attrs: attrs, Content: content}, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Encoder
// ──────────────────────────────────────────────────────────────────────────────

// EncodeNode encodes a Node into raw binary node bytes (no transport prefix).
func EncodeNode(n Node) ([]byte, error) {
	e := &encoder{}
	if err := e.writeNode(n); err != nil {
		return nil, err
	}
	return e.buf, nil
}

type encoder struct {
	buf []byte
}

func (e *encoder) pushByte(b byte) {
	e.buf = append(e.buf, b)
}

func (e *encoder) pushBytes(b []byte) {
	e.buf = append(e.buf, b...)
}

func (e *encoder) pushInt16(v int) {
	e.buf = append(e.buf, byte(v>>8), byte(v))
}

func (e *encoder) pushInt20(v int) {
	e.buf = append(e.buf, byte((v>>16)&0x0f), byte(v>>8), byte(v))
}

func (e *encoder) pushInt32(v int) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(v))
	e.buf = append(e.buf, b[:]...)
}

func (e *encoder) writeListStart(size int) {
	if size == 0 {
		e.pushByte(LIST_EMPTY)
	} else if size < 256 {
		e.pushByte(LIST_8)
		e.pushByte(byte(size))
	} else {
		e.pushByte(LIST_16)
		e.pushInt16(size)
	}
}

// writeByteLength writes the BINARY tag + length prefix for raw bytes.
func (e *encoder) writeByteLength(length int) error {
	if length >= 1<<32 {
		return fmt.Errorf("wire: string too large to encode: %d", length)
	}
	if length >= 1<<20 {
		e.pushByte(BINARY_32)
		e.pushInt32(length)
	} else if length >= 256 {
		e.pushByte(BINARY_20)
		e.pushInt20(length)
	} else {
		e.pushByte(BINARY_8)
		e.pushByte(byte(length))
	}
	return nil
}

func (e *encoder) writeStringRaw(s string) error {
	b := []byte(s)
	if err := e.writeByteLength(len(b)); err != nil {
		return err
	}
	e.pushBytes(b)
	return nil
}

// packNibble packs a character as a 4-bit nibble value.
func packNibble(c byte) (byte, error) {
	if c >= '0' && c <= '9' {
		return c - '0', nil
	}
	switch c {
	case '-':
		return 10, nil
	case '.':
		return 11, nil
	case 0:
		return 15, nil
	}
	return 0, fmt.Errorf("wire: invalid nibble char: %q", c)
}

// packHex packs a character as a 4-bit hex nibble.
func packHex(c byte) (byte, error) {
	if c >= '0' && c <= '9' {
		return c - '0', nil
	}
	if c >= 'A' && c <= 'F' {
		return 10 + c - 'A', nil
	}
	if c >= 'a' && c <= 'f' {
		return 10 + c - 'a', nil
	}
	if c == 0 {
		return 15, nil
	}
	return 0, fmt.Errorf("wire: invalid hex char: %q", c)
}

// isNibble returns true if str can be packed as nibbles (digits, '-', '.').
// Empty strings are excluded.
func isNibble(str string) bool {
	if str == "" || len(str) > int(PACKED_MAX) {
		return false
	}
	for i := 0; i < len(str); i++ {
		c := str[i]
		if (c >= '0' && c <= '9') || c == '-' || c == '.' {
			continue
		}
		return false
	}
	return true
}

// isHex returns true if str can be packed as hex nibbles (0-9, A-F only, uppercase).
// Empty strings are excluded.
func isHex(str string) bool {
	if str == "" || len(str) > int(PACKED_MAX) {
		return false
	}
	for i := 0; i < len(str); i++ {
		c := str[i]
		if (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

// writePackedBytes writes NIBBLE_8 or HEX_8 packed encoding.
func (e *encoder) writePackedBytes(str string, useNibble bool) error {
	if len(str) > int(PACKED_MAX) {
		return fmt.Errorf("wire: too many bytes to pack: %d", len(str))
	}
	if useNibble {
		e.pushByte(NIBBLE_8)
	} else {
		e.pushByte(HEX_8)
	}

	roundedLen := (len(str) + 1) / 2
	startByte := byte(roundedLen)
	if len(str)%2 != 0 {
		startByte |= 0x80
	}
	e.pushByte(startByte)

	packFn := packNibble
	if !useNibble {
		packFn = packHex
	}

	half := len(str) / 2
	for i := 0; i < half; i++ {
		hi, err := packFn(str[2*i])
		if err != nil {
			return err
		}
		lo, err := packFn(str[2*i+1])
		if err != nil {
			return err
		}
		e.pushByte((hi << 4) | lo)
	}
	if len(str)%2 != 0 {
		hi, err := packFn(str[len(str)-1])
		if err != nil {
			return err
		}
		lo, err := packFn(0) // pad with \0
		if err != nil {
			return err
		}
		e.pushByte((hi << 4) | lo)
	}
	return nil
}

// jidDecodeGo decodes a JID string into components, matching jid-utils.js jidDecode.
// Returns (user, server, device, domainType, hasDevice, ok).
type jidDecoded struct {
	user       string
	server     string
	device     int
	domainType int
	hasDevice  bool
}

func jidDecodeStr(jid string) (jidDecoded, bool) {
	sepIdx := strings.IndexByte(jid, '@')
	if sepIdx < 0 {
		return jidDecoded{}, false
	}
	server := jid[sepIdx+1:]
	userCombined := jid[:sepIdx]

	// Split on ':' for device.
	var userAgent, deviceStr string
	colonIdx := strings.IndexByte(userCombined, ':')
	hasDevice := false
	device := 0
	if colonIdx >= 0 {
		userAgent = userCombined[:colonIdx]
		deviceStr = userCombined[colonIdx+1:]
		hasDevice = true
		fmt.Sscanf(deviceStr, "%d", &device)
	} else {
		userAgent = userCombined
	}

	// Split on '_' for agent.
	var user string
	underIdx := strings.IndexByte(userAgent, '_')
	if underIdx >= 0 {
		user = userAgent[:underIdx]
	} else {
		user = userAgent
	}

	// Determine domain type.
	domainType := 0 // WHATSAPP
	switch server {
	case "lid":
		domainType = 1
	case "hosted":
		domainType = 128
	case "hosted.lid":
		domainType = 129
	}

	return jidDecoded{
		user:       user,
		server:     server,
		device:     device,
		domainType: domainType,
		hasDevice:  hasDevice,
	}, true
}

// writeJid encodes a JID string using AD_JID or JID_PAIR.
func (e *encoder) writeJid(j jidDecoded) error {
	if j.hasDevice {
		// AD_JID encoding.
		e.pushByte(AD_JID)
		e.pushByte(byte(j.domainType))
		e.pushByte(byte(j.device))
		return e.writeString(j.user)
	}
	// JID_PAIR encoding.
	e.pushByte(JID_PAIR)
	if j.user == "" {
		e.pushByte(LIST_EMPTY)
	} else {
		if err := e.writeString(j.user); err != nil {
			return err
		}
	}
	return e.writeString(j.server)
}

// writeString encodes a string value using the most compact representation.
func (e *encoder) writeString(s string) error {
	if s == "" {
		// Empty string → writeStringRaw (BINARY_8 + 0x00)
		// This matches encode.js: writeStringRaw for empty string
		return e.writeStringRaw(s)
	}

	// Check single-byte token.
	if idx, ok := tokenIndex(s); ok {
		e.pushByte(byte(idx))
		return nil
	}

	// Check double-byte token.
	if dict, idx, ok := doubleTokenIndex(s); ok {
		e.pushByte(DICTIONARY_0 + byte(dict))
		e.pushByte(byte(idx))
		return nil
	}

	// Try nibble packing.
	if isNibble(s) {
		return e.writePackedBytes(s, true)
	}

	// Try hex packing.
	if isHex(s) {
		return e.writePackedBytes(s, false)
	}

	// Try JID encoding.
	if j, ok := jidDecodeStr(s); ok {
		return e.writeJid(j)
	}

	// Fall back to raw string.
	return e.writeStringRaw(s)
}

func (e *encoder) writeNode(n Node) error {
	if n.Tag == "" {
		return fmt.Errorf("wire: node tag cannot be empty")
	}

	// Collect and sort attribute keys for deterministic encoding.
	// Go maps have non-deterministic iteration order; sorting by key ensures
	// that EncodeNode is reproducible. The original JS encoder preserves
	// insertion order (JS objects), so nodes with attrs in non-alphabetical
	// insertion order will not byte-exact match after a Go decode-encode cycle.
	attrKeys := make([]string, 0, len(n.Attrs))
	for k := range n.Attrs {
		attrKeys = append(attrKeys, k)
	}
	sort.Strings(attrKeys)

	// Determine if content is present.
	hasContent := n.Content != nil
	// But check for typed nil ([]Node(nil) etc.)
	switch c := n.Content.(type) {
	case []Node:
		hasContent = c != nil
	case []byte:
		hasContent = c != nil
	case string:
		hasContent = true // empty string is still content
	}

	contentBit := 0
	if hasContent {
		contentBit = 1
	}
	listSize := 2*len(attrKeys) + 1 + contentBit
	e.writeListStart(listSize)

	// Write tag.
	if err := e.writeString(n.Tag); err != nil {
		return fmt.Errorf("wire: tag: %w", err)
	}

	// Write attributes.
	for _, k := range attrKeys {
		v := n.Attrs[k]
		if err := e.writeString(k); err != nil {
			return fmt.Errorf("wire: attr key %q: %w", k, err)
		}
		if err := e.writeString(v); err != nil {
			return fmt.Errorf("wire: attr val %q: %w", v, err)
		}
	}

	// Write content.
	if hasContent {
		switch c := n.Content.(type) {
		case string:
			if err := e.writeString(c); err != nil {
				return fmt.Errorf("wire: string content: %w", err)
			}
		case []byte:
			if err := e.writeByteLength(len(c)); err != nil {
				return err
			}
			e.pushBytes(c)
		case []Node:
			e.writeListStart(len(c))
			for i, child := range c {
				if err := e.writeNode(child); err != nil {
					return fmt.Errorf("wire: child[%d]: %w", i, err)
				}
			}
		default:
			return fmt.Errorf("wire: unsupported content type: %T", n.Content)
		}
	}

	return nil
}
