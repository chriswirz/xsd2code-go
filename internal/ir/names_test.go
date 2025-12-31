package ir

import "testing"

func TestPascal(t *testing.T) {
	cases := map[string]string{
		"order":           "Order",
		"order-id":        "OrderId",
		"order_id":        "OrderId",
		"orderId":         "OrderId",
		"ORDER_ID":        "OrderId",
		"USAddress":       "USAddress",
		"XMLName":         "XMLName",
		"tns:AddressType": "AddressType",
		"3rdParty":        "N3rdParty",
		"":                "Item",
		"on-hold":         "OnHold",
		"N/A":             "NA",
	}
	for in, want := range cases {
		if got := Pascal(in); got != want {
			t.Errorf("Pascal(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCamel(t *testing.T) {
	cases := map[string]string{
		"order-id":  "orderId",
		"USAddress": "usAddress",
		"XMLName":   "xmlName",
		"Order":     "order",
	}
	for in, want := range cases {
		if got := Camel(in); got != want {
			t.Errorf("Camel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSnake(t *testing.T) {
	cases := map[string]string{
		"OrderId":   "order_id",
		"USAddress": "us_address",
		"Shipment":  "shipment",
		// A word Postgres reserves gets a suffix rather than a quoted-only
		// life in hand-written queries.
		"Group": "group_",
		"Order": "order_",
		"":      "value",
	}
	for in, want := range cases {
		if got := Snake(in); got != want {
			t.Errorf("Snake(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScreamingSnake(t *testing.T) {
	if got := ScreamingSnake("on-hold"); got != "ON_HOLD" {
		t.Errorf("ScreamingSnake(on-hold) = %q", got)
	}
}

func TestUniquerSuffixesCollisions(t *testing.T) {
	u := newUniquer()
	if got := u.take("Item"); got != "Item" {
		t.Fatalf("first take = %q", got)
	}
	if got := u.take("Item"); got != "Item2" {
		t.Fatalf("second take = %q", got)
	}
	// Collision detection is case-insensitive, because C# and SQL both treat
	// two names differing only in case as the same name in practice.
	if got := u.take("item"); got != "item3" {
		t.Fatalf("third take = %q", got)
	}
}
