// Package client: business.go implements WhatsApp Business read APIs — fetching
// a business profile, a product catalog, and order details. These mirror
// Baileys' Socket/business.ts (getBusinessProfile lives in chats.ts) using plain
// request iqs.
//
// Confirmed Baileys structures:
//
//	getBusinessProfile(jid)  (chats.ts):
//	  <iq to=@s.whatsapp.net type=get xmlns=w:biz>
//	    <business_profile v=244>
//	      <profile jid=<jid>/>
//	    </business_profile>
//	  </iq>
//	  reply: <iq><business_profile><profile jid=...>
//	           <address>..</address> <description>..</description>
//	           <website>..</website> <email>..</email>
//	           <business_hours timezone=..>..</business_hours>
//	           <categories><category id=..>name</category></categories>
//	         </profile></business_profile></iq>
//
//	getCatalog(jid, limit)  (business.ts):
//	  <iq to=@s.whatsapp.net type=get xmlns=w:biz:catalog>
//	    <product_catalog jid=<jid> allow_shop_source=true>
//	      <limit>N</limit> <width>100</width> <height>100</height>
//	    </product_catalog>
//	  </iq>
//	  reply parsed by parseCatalogNode: <product_catalog><product id=..>
//	    <name>..</name> <description>..</description>
//	    <price>..</price> <currency>..</currency> ...</product>
//
//	getOrderDetails(orderID, tokenBase64)  (business.ts):
//	  <iq to=@s.whatsapp.net type=get xmlns=fb:thrift_iq smax_id=5>
//	    <order op=get id=<orderID>>
//	      <image_dimensions><width>100</width><height>100</height></image_dimensions>
//	      <token>...base64...</token>
//	    </order>
//	  </iq>
//	  reply parsed by parseOrderDetailsNode: <order><products><product id=..>
//	    <name>..</name> <price>..</price> <currency>..</currency>
//	    <quantity>..</quantity> ...</product></products>
//	    <price><total>..</total><currency>..</currency></price></order>
//
// The iq builders are pure functions so their structure/attributes can be
// asserted offline; the public methods wire them through c.sendIQ on the live
// session and parse the reply with the pure parsers below.
package client

import (
	"context"
	"errors"
	"strconv"

	"github.com/jfelipesjc/wa-go/internal/wire"
)

// catalogWidth/catalogHeight are the thumbnail dimensions Baileys requests for
// catalog/order images.
const (
	catalogImageWidth  = "100"
	catalogImageHeight = "100"
)

// BusinessProfile is the parsed read of a business account's profile.
type BusinessProfile struct {
	JID         string
	Address     string
	Description string
	Website     string
	Email       string
	// BusinessHoursTimezone is the timezone attr of <business_hours>.
	BusinessHoursTimezone string
	// Categories are the business category names listed under <categories>.
	Categories []string
}

// Product is one catalog entry / order line item.
type Product struct {
	ID          string
	Name        string
	Description string
	// Price is the integer price in the currency's minor units (Baileys keeps the
	// raw string; we parse to int64 when possible, leaving PriceRaw for fidelity).
	Price      int64
	PriceRaw   string
	Currency   string
	Quantity   int64
	RetailerID string
}

// OrderDetails is the parsed read of an order: its line items and total.
type OrderDetails struct {
	Products      []Product
	TotalRaw      string
	Total         int64
	TotalCurrency string
}

// businessProfileNode builds the get-business-profile iq (Baileys chats.ts):
//
//	<iq to=@s.whatsapp.net type=get xmlns=w:biz id=...>
//	  <business_profile v=244><profile jid=<jid>/></business_profile>
//	</iq>
func businessProfileNode(id, jid string) wire.Node {
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    sWhatsAppNet,
			"type":  "get",
			"xmlns": "w:biz",
			"id":    id,
		},
		Content: []wire.Node{
			{
				Tag:   "business_profile",
				Attrs: map[string]string{"v": "244"},
				Content: []wire.Node{
					{Tag: "profile", Attrs: map[string]string{"jid": jid}},
				},
			},
		},
	}
}

// catalogNode builds the get-catalog iq (Baileys business.ts):
//
//	<iq to=@s.whatsapp.net type=get xmlns=w:biz:catalog id=...>
//	  <product_catalog jid=<jid> allow_shop_source=true>
//	    <limit>N</limit><width>100</width><height>100</height>
//	  </product_catalog>
//	</iq>
func catalogNode(id, jid string, limit int) wire.Node {
	if limit <= 0 {
		limit = 10
	}
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":    sWhatsAppNet,
			"type":  "get",
			"xmlns": "w:biz:catalog",
			"id":    id,
		},
		Content: []wire.Node{
			{
				Tag:   "product_catalog",
				Attrs: map[string]string{"jid": jid, "allow_shop_source": "true"},
				Content: []wire.Node{
					{Tag: "limit", Content: []byte(strconv.Itoa(limit))},
					{Tag: "width", Content: []byte(catalogImageWidth)},
					{Tag: "height", Content: []byte(catalogImageHeight)},
				},
			},
		},
	}
}

// orderDetailsNode builds the get-order iq (Baileys business.ts):
//
//	<iq to=@s.whatsapp.net type=get xmlns=fb:thrift_iq smax_id=5 id=...>
//	  <order op=get id=<orderID>>
//	    <image_dimensions><width>100</width><height>100</height></image_dimensions>
//	    <token>...base64...</token>
//	  </order>
//	</iq>
func orderDetailsNode(id, orderID, tokenBase64 string) wire.Node {
	return wire.Node{
		Tag: "iq",
		Attrs: map[string]string{
			"to":      sWhatsAppNet,
			"type":    "get",
			"xmlns":   "fb:thrift_iq",
			"smax_id": "5",
			"id":      id,
		},
		Content: []wire.Node{
			{
				Tag:   "order",
				Attrs: map[string]string{"op": "get", "id": orderID},
				Content: []wire.Node{
					{
						Tag: "image_dimensions",
						Content: []wire.Node{
							{Tag: "width", Content: []byte(catalogImageWidth)},
							{Tag: "height", Content: []byte(catalogImageHeight)},
						},
					},
					{Tag: "token", Content: []byte(tokenBase64)},
				},
			},
		},
	}
}

// childText returns the leaf string content of the named child, or "".
func childText(n wire.Node, tag string) string {
	ch, ok := childByTag(n, tag)
	if !ok {
		return ""
	}
	return string(nodeBytes(ch))
}

// parseBusinessProfile parses a get-business-profile reply into a BusinessProfile.
// It tolerates a missing profile node (returns an error) and missing fields
// (left empty).
func parseBusinessProfile(reply wire.Node) (*BusinessProfile, error) {
	bp, ok := childByTag(reply, "business_profile")
	if !ok {
		return nil, errors.New("client: business profile reply missing <business_profile>")
	}
	prof, ok := childByTag(bp, "profile")
	if !ok {
		return nil, errors.New("client: business profile reply missing <profile>")
	}
	out := &BusinessProfile{
		JID:         prof.Attrs["jid"],
		Address:     childText(prof, "address"),
		Description: childText(prof, "description"),
		Website:     childText(prof, "website"),
		Email:       childText(prof, "email"),
	}
	if hours, ok := childByTag(prof, "business_hours"); ok {
		out.BusinessHoursTimezone = hours.Attrs["timezone"]
	}
	if cats, ok := childByTag(prof, "categories"); ok {
		for _, cat := range childrenByTag(cats, "category") {
			if name := string(nodeBytes(cat)); name != "" {
				out.Categories = append(out.Categories, name)
			}
		}
	}
	return out, nil
}

// parseProduct parses a single <product id=..> node into a Product.
func parseProduct(p wire.Node) Product {
	prod := Product{
		ID:          p.Attrs["id"],
		Name:        childText(p, "name"),
		Description: childText(p, "description"),
		PriceRaw:    childText(p, "price"),
		Currency:    childText(p, "currency"),
		RetailerID:  childText(p, "retailer_id"),
	}
	if prod.PriceRaw != "" {
		if v, err := strconv.ParseInt(prod.PriceRaw, 10, 64); err == nil {
			prod.Price = v
		}
	}
	if q := childText(p, "quantity"); q != "" {
		if v, err := strconv.ParseInt(q, 10, 64); err == nil {
			prod.Quantity = v
		}
	}
	return prod
}

// parseCatalog parses a get-catalog reply into a Product slice, mirroring
// Baileys' parseCatalogNode: <product_catalog><product id=..>...</product>.
func parseCatalog(reply wire.Node) ([]Product, error) {
	cat, ok := childByTag(reply, "product_catalog")
	if !ok {
		return nil, errors.New("client: catalog reply missing <product_catalog>")
	}
	var out []Product
	for _, p := range childrenByTag(cat, "product") {
		out = append(out, parseProduct(p))
	}
	return out, nil
}

// parseOrderDetails parses a get-order reply into an OrderDetails, mirroring
// Baileys' parseOrderDetailsNode: <order><products><product>...; <price>.
func parseOrderDetails(reply wire.Node) (*OrderDetails, error) {
	order, ok := childByTag(reply, "order")
	if !ok {
		return nil, errors.New("client: order reply missing <order>")
	}
	out := &OrderDetails{}
	if products, ok := childByTag(order, "products"); ok {
		for _, p := range childrenByTag(products, "product") {
			out.Products = append(out.Products, parseProduct(p))
		}
	}
	if price, ok := childByTag(order, "price"); ok {
		out.TotalRaw = childText(price, "total")
		out.TotalCurrency = childText(price, "currency")
		if out.TotalRaw != "" {
			if v, err := strconv.ParseInt(out.TotalRaw, 10, 64); err == nil {
				out.Total = v
			}
		}
	}
	return out, nil
}

// GetBusinessProfile fetches the business profile for jid.
func (c *Client) GetBusinessProfile(ctx context.Context, jid string) (*BusinessProfile, error) {
	if jid == "" {
		return nil, errors.New("client: GetBusinessProfile requires a jid")
	}
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: GetBusinessProfile requires a live session")
	}
	reply, err := c.sendIQ(ctx, sess, businessProfileNode(c.nextIQID("biz"), jid))
	if err != nil {
		return nil, err
	}
	return parseBusinessProfile(reply)
}

// GetCatalog fetches up to limit products from the business catalog of jid.
func (c *Client) GetCatalog(ctx context.Context, jid string, limit int) ([]Product, error) {
	if jid == "" {
		return nil, errors.New("client: GetCatalog requires a jid")
	}
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: GetCatalog requires a live session")
	}
	reply, err := c.sendIQ(ctx, sess, catalogNode(c.nextIQID("biz"), jid, limit))
	if err != nil {
		return nil, err
	}
	return parseCatalog(reply)
}

// GetOrderDetails fetches the details of an order. tokenBase64 is the order's
// access token (the base64 token WhatsApp attaches to an order message), which
// the server requires to authorize the read.
func (c *Client) GetOrderDetails(ctx context.Context, orderID, tokenBase64 string) (*OrderDetails, error) {
	if orderID == "" {
		return nil, errors.New("client: GetOrderDetails requires an orderID")
	}
	sess, ok := c.activeSession()
	if !ok {
		return nil, errors.New("client: GetOrderDetails requires a live session")
	}
	reply, err := c.sendIQ(ctx, sess, orderDetailsNode(c.nextIQID("biz"), orderID, tokenBase64))
	if err != nil {
		return nil, err
	}
	return parseOrderDetails(reply)
}
