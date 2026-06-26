// Package client: business_write.go implements WhatsApp Business catalog WRITE
// APIs — creating, updating and deleting products. These mirror Baileys'
// Socket/business.ts (productCreate / productUpdate / productDelete) and the
// node builder Utils/business.ts:toProductNode.
//
// Confirmed Baileys structures (all over xmlns="w:biz:catalog", type="set"):
//
//	productCreate(create):  toProductNode(undefined, create)
//	  <iq to=@s.whatsapp.net type=set xmlns=w:biz:catalog id=...>
//	    <product_catalog_add v=1>
//	      <product [is_hidden=..] [compliance_category=..]>
//	        <name>..</name> <description>..</description>
//	        <retailer_id>..</retailer_id>
//	        <media><image><url>..</url></image>...</media>
//	        <price>..</price> <currency>..</currency>
//	        <compliance_info><country_code_origin>..</country_code_origin></compliance_info>
//	      </product>
//	      <width>100</width> <height>100</height>
//	    </product_catalog_add>
//	  </iq>
//	  reply: <product_catalog_add><product ...> -> parseProductNode
//
//	productUpdate(productId, update):  toProductNode(productId, update)
//	  identical to create but wrapper tag is <product_catalog_edit v=1> and the
//	  <product> carries a leading <id>..</id> child.
//	  reply: <product_catalog_edit><product ...> -> parseProductNode
//
//	productDelete(productIds):
//	  <iq to=@s.whatsapp.net type=set xmlns=w:biz:catalog id=...>
//	    <product_catalog_delete v=1>
//	      <product><id>..</id></product> ...
//	    </product_catalog_delete>
//	  </iq>
//	  reply: <product_catalog_delete deleted_count=N/> -> {deleted: N}
//
// The order of <product> children mirrors Baileys' toProductNode exactly:
// id, name, description, retailer_id, media, price, currency, compliance_info.
//
// IMAGE NOTE: Baileys runs uploadingNecessaryImagesOfProduct first, turning each
// input image into an already-uploaded {url} that ends in ".whatsapp.net" before
// emitting <media><image><url>. We do NOT perform media upload here (no upload
// pipeline is in scope); callers must pass image URLs that are already hosted on
// WhatsApp's CDN. The builders place whatever URL strings are supplied verbatim,
// exactly as Baileys would after upload. This is the one documented partial: the
// caller owns image upload.
//
// All node builders are pure functions so their structure/attributes can be
// asserted offline; the public methods wire them through c.sendIQ on the live
// session and parse the reply with parseProduct (reused from business.go).
package client

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jfelipesjc/wa-go/internal/wire"
)

// ProductCreateInput holds the fields Baileys' productCreate accepts. Mirrors
// toProductNode's recognised keys (name, description, retailerId, images, price,
// currency, isHidden). Price is the integer price in the currency's minor units
// (Baileys stringifies it).
type ProductCreateInput struct {
	Name        string
	Description string
	// Price is the integer price (minor units). It is always emitted for create.
	Price    int64
	Currency string
	// RetailerID is the merchant's own SKU/identifier for the product.
	RetailerID string
	// Images are URLs already hosted on WhatsApp's CDN (see IMAGE NOTE above).
	Images []string
	// IsHidden hides the product from the public catalog. Baileys forces this to
	// a defined bool on create, so it is always emitted as is_hidden.
	IsHidden bool
}

// ProductUpdateInput holds the fields for productUpdate. Every field is a
// pointer so callers can express "leave unchanged" (nil) versus "set to this
// value", matching Baileys where toProductNode only emits defined keys. Images
// nil means "do not touch media"; a non-nil (possibly empty) slice replaces it.
type ProductUpdateInput struct {
	Name        *string
	Description *string
	Price       *int64
	Currency    *string
	RetailerID  *string
	Images      []string
	IsHidden    *bool
}

// productCreateContentNode builds the <product> node for a create, mirroring
// Baileys toProductNode(undefined, create): no <id>, is_hidden always set,
// price/currency always emitted.
func productCreateContentNode(in ProductCreateInput) wire.Node {
	attrs := map[string]string{}
	var content []wire.Node

	if in.Name != "" {
		content = append(content, wire.Node{Tag: "name", Content: []byte(in.Name)})
	}
	if in.Description != "" {
		content = append(content, wire.Node{Tag: "description", Content: []byte(in.Description)})
	}
	if in.RetailerID != "" {
		content = append(content, wire.Node{Tag: "retailer_id", Content: []byte(in.RetailerID)})
	}
	if media, ok := productMediaNode(in.Images); ok {
		content = append(content, media)
	}
	// Baileys always emits price/currency when defined; create always defines them.
	content = append(content, wire.Node{Tag: "price", Content: []byte(strconv.FormatInt(in.Price, 10))})
	if in.Currency != "" {
		content = append(content, wire.Node{Tag: "currency", Content: []byte(in.Currency)})
	}
	// create.isHidden is coerced to a defined bool by Baileys, so always emit it.
	attrs["is_hidden"] = strconv.FormatBool(in.IsHidden)

	return wire.Node{Tag: "product", Attrs: attrs, Content: content}
}

// productUpdateContentNode builds the <product> node for an edit, mirroring
// Baileys toProductNode(productId, update): leading <id>, and only fields that
// are set (non-nil) are emitted.
func productUpdateContentNode(productID string, in ProductUpdateInput) wire.Node {
	attrs := map[string]string{}
	var content []wire.Node

	content = append(content, wire.Node{Tag: "id", Content: []byte(productID)})
	if in.Name != nil {
		content = append(content, wire.Node{Tag: "name", Content: []byte(*in.Name)})
	}
	if in.Description != nil {
		content = append(content, wire.Node{Tag: "description", Content: []byte(*in.Description)})
	}
	if in.RetailerID != nil {
		content = append(content, wire.Node{Tag: "retailer_id", Content: []byte(*in.RetailerID)})
	}
	if in.Images != nil {
		if media, ok := productMediaNode(in.Images); ok {
			content = append(content, media)
		}
	}
	if in.Price != nil {
		content = append(content, wire.Node{Tag: "price", Content: []byte(strconv.FormatInt(*in.Price, 10))})
	}
	if in.Currency != nil {
		content = append(content, wire.Node{Tag: "currency", Content: []byte(*in.Currency)})
	}
	if in.IsHidden != nil {
		attrs["is_hidden"] = strconv.FormatBool(*in.IsHidden)
	}

	return wire.Node{Tag: "product", Attrs: attrs, Content: content}
}

// productMediaNode builds <media><image><url>..</url></image>...</media> from a
// list of (already-uploaded) image URLs. Returns ok=false when there are no
// usable images, so the caller omits the <media> node entirely (matching
// Baileys, which only emits <media> when product.images.length).
func productMediaNode(images []string) (wire.Node, bool) {
	var imgs []wire.Node
	for _, u := range images {
		if strings.TrimSpace(u) == "" {
			continue
		}
		imgs = append(imgs, wire.Node{
			Tag:     "image",
			Content: []wire.Node{{Tag: "url", Content: []byte(u)}},
		})
	}
	if len(imgs) == 0 {
		return wire.Node{}, false
	}
	return wire.Node{Tag: "media", Content: imgs}, true
}

// productCreateNode builds the full create iq.
func productCreateNode(id string, in ProductCreateInput) wire.Node {
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    sWhatsAppNet,
			"type":  "set",
			"xmlns": "w:biz:catalog",
			"id":    id,
		},
		Content: []wire.Node{
			{
				Tag:   "product_catalog_add",
				Attrs: map[string]string{"v": "1"},
				Content: []wire.Node{
					productCreateContentNode(in),
					{Tag: "width", Content: []byte(catalogImageWidth)},
					{Tag: "height", Content: []byte(catalogImageHeight)},
				},
			},
		},
	}
}

// productUpdateNode builds the full edit iq.
func productUpdateNode(id, productID string, in ProductUpdateInput) wire.Node {
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    sWhatsAppNet,
			"type":  "set",
			"xmlns": "w:biz:catalog",
			"id":    id,
		},
		Content: []wire.Node{
			{
				Tag:   "product_catalog_edit",
				Attrs: map[string]string{"v": "1"},
				Content: []wire.Node{
					productUpdateContentNode(productID, in),
					{Tag: "width", Content: []byte(catalogImageWidth)},
					{Tag: "height", Content: []byte(catalogImageHeight)},
				},
			},
		},
	}
}

// productDeleteNode builds the full delete iq.
func productDeleteNode(id string, productIDs []string) wire.Node {
	var products []wire.Node
	for _, pid := range productIDs {
		products = append(products, wire.Node{
			Tag:     "product",
			Content: []wire.Node{{Tag: "id", Content: []byte(pid)}},
		})
	}
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    sWhatsAppNet,
			"type":  "set",
			"xmlns": "w:biz:catalog",
			"id":    id,
		},
		Content: []wire.Node{
			{
				Tag:     "product_catalog_delete",
				Attrs:   map[string]string{"v": "1"},
				Content: products,
			},
		},
	}
}

// parseProductWriteReply extracts the echoed <product> from a create/update
// reply. wrapperTag is "product_catalog_add" or "product_catalog_edit". Mirrors
// Baileys: getBinaryNodeChild(result, wrapper) -> child "product" -> parseProductNode.
func parseProductWriteReply(reply wire.Node, wrapperTag string) (*Product, error) {
	wrap, ok := childByTag(reply, wrapperTag)
	if !ok {
		return nil, errors.New("client: product write reply missing <" + wrapperTag + ">")
	}
	prodNode, ok := childByTag(wrap, "product")
	if !ok {
		return nil, errors.New("client: product write reply missing <product>")
	}
	p := parseProduct(prodNode)
	return &p, nil
}

// parseProductDeleteReply reads the deleted_count attribute from a delete reply,
// mirroring Baileys: getBinaryNodeChild(result, 'product_catalog_delete')
// .attrs.deleted_count.
func parseProductDeleteReply(reply wire.Node) (int, error) {
	del, ok := childByTag(reply, "product_catalog_delete")
	if !ok {
		return 0, errors.New("client: product delete reply missing <product_catalog_delete>")
	}
	raw := del.Attrs["deleted_count"]
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errors.New("client: product delete reply has invalid deleted_count " + strconv.Quote(raw))
	}
	return n, nil
}

// ProductCreate adds a product to the business catalog. The returned Product is
// the server's echo of the created entry. Image URLs in product.Images must
// already be hosted on WhatsApp's CDN (see the IMAGE NOTE at the top of the file).
func (c *Client) ProductCreate(ctx context.Context, product ProductCreateInput) (*Product, error) {
	if product.Name == "" {
		return nil, errors.New("client: ProductCreate requires a name")
	}
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: ProductCreate requires a live session")
	}
	reply, err := c.sendIQ(ctx, sess, productCreateNode(c.nextIQID("biz"), product))
	if err != nil {
		return nil, err
	}
	return parseProductWriteReply(reply, "product_catalog_add")
}

// ProductUpdate edits an existing catalog product. Only the set (non-nil) fields
// of product are changed. The returned Product is the server's echo of the
// updated entry.
func (c *Client) ProductUpdate(ctx context.Context, productID string, product ProductUpdateInput) (*Product, error) {
	if productID == "" {
		return nil, errors.New("client: ProductUpdate requires a productID")
	}
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: ProductUpdate requires a live session")
	}
	reply, err := c.sendIQ(ctx, sess, productUpdateNode(c.nextIQID("biz"), productID, product))
	if err != nil {
		return nil, err
	}
	return parseProductWriteReply(reply, "product_catalog_edit")
}

// ProductDelete removes products from the catalog by ID and returns the number
// the server reports as deleted.
func (c *Client) ProductDelete(ctx context.Context, productIDs []string) (int, error) {
	if len(productIDs) == 0 {
		return 0, errors.New("client: ProductDelete requires at least one productID")
	}
	sess, ok := c.activeSession()
	if !ok {
		return 0, errors.New("client: ProductDelete requires a live session")
	}
	reply, err := c.sendIQ(ctx, sess, productDeleteNode(c.nextIQID("biz"), productIDs))
	if err != nil {
		return 0, err
	}
	return parseProductDeleteReply(reply)
}
