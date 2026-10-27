package dto

type CreateOrderRequest struct {
	ListingID string `json:"listing_id" validate:"required"`
	Qty       int    `json:"qty" validate:"min=1"`
}

type OrderResponse struct {
	ID        string `json:"id"`
	BuyerID   string `json:"buyer_id"`
	ListingID string `json:"listing_id"`
	Qty       int    `json:"qty"`
	CreatedAt string `json:"created_at"`
}

type CreateOrderResponse struct {
	Replayed  bool   `json:"-"`
	ID        string `json:"id"`
	BuyerID   string `json:"buyer_id"`
	ListingID string `json:"listing_id"`
	Qty       int    `json:"qty"`
	CreatedAt string `json:"created_at"`
}

type ListRequest struct {
}

type ListResponse struct {
	Orders []OrderResponse
	Total  int64
}
