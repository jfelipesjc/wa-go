// Package wire defines the binary node tree used by the WhatsApp wire protocol.
package wire

// Node represents a single element in the WhatsApp binary encoding tree.
// Tag is the element name, Attrs holds key-value attributes, and Content
// is either a []Node (child nodes), []byte (leaf bytes), or nil.
type Node struct {
	Tag     string
	Attrs   map[string]string
	Content any
}
