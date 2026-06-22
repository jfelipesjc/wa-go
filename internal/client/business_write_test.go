package client

import (
	"testing"

	"github.com/felipeleal/wa-go/internal/wire"
)

func strPtr(s string) *string { return &s }
func i64Ptr(v int64) *int64   { return &v }
func boolPtr(b bool) *bool    { return &b }

// --- create node ---

func TestProductCreateNode(t *testing.T) {
	n := productCreateNode("w1", ProductCreateInput{
		Name:        "Widget",
		Description: "A fine widget",
		Price:       1990,
		Currency:    "BRL",
		RetailerID:  "SKU-1",
		Images:      []string{"https://mmg.whatsapp.net/x.jpg"},
		IsHidden:    true,
	})
	attr(t, n, "to", sWhatsAppNet)
	attr(t, n, "type", "set")
	attr(t, n, "xmlns", "w:biz:catalog")
	attr(t, n, "id", "w1")

	add, ok := childByTag(n, "product_catalog_add")
	if !ok {
		t.Fatal("missing <product_catalog_add>")
	}
	attr(t, add, "v", "1")
	if childText(add, "width") != "100" || childText(add, "height") != "100" {
		t.Fatal("missing width/height 100")
	}

	prod, ok := childByTag(add, "product")
	if !ok {
		t.Fatal("missing <product>")
	}
	attr(t, prod, "is_hidden", "true")
	if childText(prod, "name") != "Widget" {
		t.Fatalf("name = %q", childText(prod, "name"))
	}
	if childText(prod, "description") != "A fine widget" {
		t.Fatalf("description = %q", childText(prod, "description"))
	}
	if childText(prod, "retailer_id") != "SKU-1" {
		t.Fatalf("retailer_id = %q", childText(prod, "retailer_id"))
	}
	if childText(prod, "price") != "1990" {
		t.Fatalf("price = %q", childText(prod, "price"))
	}
	if childText(prod, "currency") != "BRL" {
		t.Fatalf("currency = %q", childText(prod, "currency"))
	}

	media, ok := childByTag(prod, "media")
	if !ok {
		t.Fatal("missing <media>")
	}
	img, ok := childByTag(media, "image")
	if !ok {
		t.Fatal("missing <image>")
	}
	if childText(img, "url") != "https://mmg.whatsapp.net/x.jpg" {
		t.Fatalf("url = %q", childText(img, "url"))
	}

	// <product> child order must mirror Baileys toProductNode:
	// name, description, retailer_id, media, price, currency.
	tags := childTags(prod)
	want := []string{"name", "description", "retailer_id", "media", "price", "currency"}
	if !equalStrs(tags, want) {
		t.Fatalf("child order = %v want %v", tags, want)
	}
}

func TestProductCreateNode_IsHiddenFalseAlwaysEmitted(t *testing.T) {
	// Baileys coerces create.isHidden to a defined bool, so it is always present.
	n := productCreateNode("w2", ProductCreateInput{Name: "X", Price: 0, Currency: "USD"})
	add, _ := childByTag(n, "product_catalog_add")
	prod, _ := childByTag(add, "product")
	attr(t, prod, "is_hidden", "false")
	// price always emitted even when zero.
	if childText(prod, "price") != "0" {
		t.Fatalf("price = %q (want 0)", childText(prod, "price"))
	}
	// no images -> no <media>.
	if _, ok := childByTag(prod, "media"); ok {
		t.Fatal("unexpected <media> with no images")
	}
}

// --- update node ---

func TestProductUpdateNode_PartialFields(t *testing.T) {
	n := productUpdateNode("w3", "P1", ProductUpdateInput{
		Name:  strPtr("New Name"),
		Price: i64Ptr(2500),
	})
	attr(t, n, "xmlns", "w:biz:catalog")
	attr(t, n, "type", "set")

	edit, ok := childByTag(n, "product_catalog_edit")
	if !ok {
		t.Fatal("missing <product_catalog_edit>")
	}
	attr(t, edit, "v", "1")
	if childText(edit, "width") != "100" || childText(edit, "height") != "100" {
		t.Fatal("missing width/height")
	}

	prod, ok := childByTag(edit, "product")
	if !ok {
		t.Fatal("missing <product>")
	}
	// leading <id> child.
	if childText(prod, "id") != "P1" {
		t.Fatalf("id = %q", childText(prod, "id"))
	}
	if childText(prod, "name") != "New Name" {
		t.Fatalf("name = %q", childText(prod, "name"))
	}
	if childText(prod, "price") != "2500" {
		t.Fatalf("price = %q", childText(prod, "price"))
	}
	// unset fields must be absent.
	if _, ok := childByTag(prod, "description"); ok {
		t.Fatal("description should be absent when nil")
	}
	if _, ok := childByTag(prod, "currency"); ok {
		t.Fatal("currency should be absent when nil")
	}
	if _, ok := prod.Attrs["is_hidden"]; ok {
		t.Fatal("is_hidden must be absent when nil")
	}
	// child order: id, name, price.
	if !equalStrs(childTags(prod), []string{"id", "name", "price"}) {
		t.Fatalf("child order = %v", childTags(prod))
	}
}

func TestProductUpdateNode_IsHiddenAndImages(t *testing.T) {
	n := productUpdateNode("w4", "P9", ProductUpdateInput{
		IsHidden: boolPtr(false),
		Images:   []string{"https://x.whatsapp.net/a.jpg", "  ", "https://x.whatsapp.net/b.jpg"},
	})
	edit, _ := childByTag(n, "product_catalog_edit")
	prod, _ := childByTag(edit, "product")
	attr(t, prod, "is_hidden", "false")
	media, ok := childByTag(prod, "media")
	if !ok {
		t.Fatal("missing <media>")
	}
	imgs := childrenByTag(media, "image")
	if len(imgs) != 2 {
		t.Fatalf("got %d images (blank should be skipped)", len(imgs))
	}
}

func TestProductUpdateNode_EmptyImagesSliceClearsMedia(t *testing.T) {
	// Non-nil but empty/blank-only slice -> no <media> emitted (no usable images).
	n := productUpdateNode("w5", "P1", ProductUpdateInput{Images: []string{}})
	edit, _ := childByTag(n, "product_catalog_edit")
	prod, _ := childByTag(edit, "product")
	if _, ok := childByTag(prod, "media"); ok {
		t.Fatal("empty images slice must not produce <media>")
	}
}

// --- delete node ---

func TestProductDeleteNode(t *testing.T) {
	n := productDeleteNode("w6", []string{"P1", "P2"})
	attr(t, n, "xmlns", "w:biz:catalog")
	attr(t, n, "type", "set")
	del, ok := childByTag(n, "product_catalog_delete")
	if !ok {
		t.Fatal("missing <product_catalog_delete>")
	}
	attr(t, del, "v", "1")
	prods := childrenByTag(del, "product")
	if len(prods) != 2 {
		t.Fatalf("got %d products", len(prods))
	}
	if childText(prods[0], "id") != "P1" || childText(prods[1], "id") != "P2" {
		t.Fatalf("ids = %q %q", childText(prods[0], "id"), childText(prods[1], "id"))
	}
}

// --- reply parsing ---

func TestParseProductWriteReply_Add(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{Tag: "product_catalog_add", Content: []wire.Node{
				{Tag: "product", Attrs: map[string]string{"id": "NEWID"}, Content: []wire.Node{
					{Tag: "name", Content: []byte("Widget")},
					{Tag: "price", Content: []byte("1990")},
					{Tag: "currency", Content: []byte("BRL")},
					{Tag: "retailer_id", Content: []byte("SKU-1")},
				}},
			}},
		},
	}
	p, err := parseProductWriteReply(reply, "product_catalog_add")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "NEWID" || p.Name != "Widget" || p.Price != 1990 || p.Currency != "BRL" || p.RetailerID != "SKU-1" {
		t.Fatalf("product = %+v", p)
	}
}

func TestParseProductWriteReply_Edit(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{Tag: "product_catalog_edit", Content: []wire.Node{
				{Tag: "product", Attrs: map[string]string{"id": "P1"}, Content: []wire.Node{
					{Tag: "name", Content: []byte("Renamed")},
				}},
			}},
		},
	}
	p, err := parseProductWriteReply(reply, "product_catalog_edit")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "P1" || p.Name != "Renamed" {
		t.Fatalf("product = %+v", p)
	}
}

func TestParseProductWriteReply_Errors(t *testing.T) {
	if _, err := parseProductWriteReply(wire.Node{Tag: "iq"}, "product_catalog_add"); err == nil {
		t.Fatal("expected error for missing wrapper")
	}
	noProduct := wire.Node{Tag: "iq", Content: []wire.Node{{Tag: "product_catalog_add"}}}
	if _, err := parseProductWriteReply(noProduct, "product_catalog_add"); err == nil {
		t.Fatal("expected error for missing <product>")
	}
}

func TestParseProductDeleteReply(t *testing.T) {
	reply := wire.Node{
		Tag: "iq",
		Content: []wire.Node{
			{Tag: "product_catalog_delete", Attrs: map[string]string{"deleted_count": "3"}},
		},
	}
	n, err := parseProductDeleteReply(reply)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("deleted = %d", n)
	}

	// missing deleted_count -> 0, no error.
	reply0 := wire.Node{Tag: "iq", Content: []wire.Node{{Tag: "product_catalog_delete"}}}
	if n, err := parseProductDeleteReply(reply0); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}

	// invalid deleted_count -> error.
	replyBad := wire.Node{Tag: "iq", Content: []wire.Node{
		{Tag: "product_catalog_delete", Attrs: map[string]string{"deleted_count": "x"}},
	}}
	if _, err := parseProductDeleteReply(replyBad); err == nil {
		t.Fatal("expected error for invalid deleted_count")
	}

	// missing wrapper -> error.
	if _, err := parseProductDeleteReply(wire.Node{Tag: "iq"}); err == nil {
		t.Fatal("expected error for missing wrapper")
	}
}

// --- helpers ---

// childTags returns the ordered list of child tags of n (skipping leaf content).
func childTags(n wire.Node) []string {
	var out []string
	for _, c := range children(n) {
		out = append(out, c.Tag)
	}
	return out
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
