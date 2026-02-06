package content_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/rbaliyan/mailbox"
	"github.com/rbaliyan/mailbox/content"
	"github.com/rbaliyan/mailbox/store"
	"github.com/rbaliyan/mailbox/store/memory"
)

// ExampleEncode demonstrates encoding a JSON struct into body + metadata.
func ExampleEncode() {
	type SensorReading struct {
		Temperature int    `json:"temperature"`
		Unit        string `json:"unit"`
	}

	reading := SensorReading{Temperature: 72, Unit: "F"}
	data, _ := json.Marshal(reading)

	body, meta, _ := content.Encode(content.JSON, data, content.WithSchema("sensor.reading/v1"))

	fmt.Println("body:", body)
	fmt.Println("content_type:", meta[content.MetaContentType])
	fmt.Println("schema:", meta[content.MetaSchema])
	// Output:
	// body: {"temperature":72,"unit":"F"}
	// content_type: application/json
	// schema: sensor.reading/v1
}

// This example demonstrates two services communicating through mailbox
// using structured JSON messages.
//
// The order-service encodes an OrderPlaced struct into the message body
// with content-type and schema metadata. The fulfillment-service receives
// the message, inspects the metadata to determine the format, and decodes
// the body back to the original struct.
//
// The same pattern works with binary formats: swap content.JSON for
// content.Protobuf or content.MsgPack. Binary codecs base64-encode the
// body automatically. The receiving side uses the registry to pick the
// right decoder based on the content_type metadata.
func Example_serviceToService() {
	ctx := context.Background()

	// -- shared types (both services import these) --

	type OrderPlaced struct {
		OrderID string `json:"order_id"`
		UserID  string `json:"user_id"`
		Total   int    `json:"total_cents"`
	}

	const orderSchema = "order.placed/v1"

	// -- infrastructure setup --

	svc, err := mailbox.NewService(mailbox.WithStore(memory.New()))
	if err != nil {
		log.Fatal(err)
	}
	if err := svc.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer svc.Close(ctx)

	// -- order-service: sends a structured message --

	order := OrderPlaced{
		OrderID: "ord-123",
		UserID:  "user-456",
		Total:   4999,
	}
	payload, _ := json.Marshal(order)

	// Encode returns body + metadata. Swap content.JSON for
	// content.Protobuf or content.MsgPack for binary formats.
	body, meta, err := content.Encode(content.JSON, payload, content.WithSchema(orderSchema))
	if err != nil {
		log.Fatal(err)
	}

	sender := svc.Client("order-service")
	draft, _ := sender.Compose()
	draft.SetSubject("New Order").SetRecipients("fulfillment-service").SetBody(body)
	for k, v := range meta {
		draft.SetMetadata(k, v)
	}
	if _, err := draft.Send(ctx); err != nil {
		log.Fatal(err)
	}

	// -- fulfillment-service: receives and decodes the message --

	registry := content.DefaultRegistry()
	receiver := svc.Client("fulfillment-service")
	inbox, _ := receiver.Folder(ctx, store.FolderInbox, mailbox.ListOptions{})

	for _, msg := range inbox.All() {
		ct := content.ContentType(msg)
		schema := content.Schema(msg)

		// Route by content type and schema
		if ct == "application/json" && schema == orderSchema {
			raw, err := content.Decode(msg, registry)
			if err != nil {
				log.Fatal(err)
			}

			var received OrderPlaced
			if err := json.Unmarshal(raw, &received); err != nil {
				log.Fatal(err)
			}

			fmt.Println("order_id:", received.OrderID)
			fmt.Println("user_id:", received.UserID)
			fmt.Println("total_cents:", received.Total)
			fmt.Println("content_type:", ct)
			fmt.Println("schema:", schema)
		}
	}

	// Output:
	// order_id: ord-123
	// user_id: user-456
	// total_cents: 4999
	// content_type: application/json
	// schema: order.placed/v1
}
