package model

type Order struct {
	ID        string
	BuyerID   string
	ListingID string
	Qty       int
	CreatedAt string
}

type CreateOrderEvent struct {
	EventID   string `json:"event_id"`
	OrderID   string `json:"order_id"`
	BuyerID   string `json:"buyer_id"`
	ListingID string `json:"listing_id"`
	Qty       int    `json:"qty"`
	CreatedAt string `json:"created_at"`
}

type OutboxEvent struct {
	ID      int64
	Topic   string
	Key     string
	Payload []byte
}

type IdempotencyKeyState struct {
	OrderID *string
}
