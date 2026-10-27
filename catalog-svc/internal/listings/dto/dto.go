package dto

type CreateListingRequest struct {
	SellerID string `json:"seller_id" validate:"required,uuid"`
	Title    string `json:"title" validate:"required,max=200"`
	Price    int64  `json:"price" validate:"gt=0"`
	Stock    *int64 `json:"stock" validate:"required,gte=0"`
}

type GetListingRequest struct {
	ID string `json:"id" validate:"required,uuid"`
}

type ListRequest struct {
	Limit  *int `json:"limit,omitempty" validate:"omitempty,min=1,max=100"`
	Offset *int `json:"offset,omitempty" validate:"omitempty,gte=0"`
}

type UpdateListingRequest struct {
	ID       string  `json:"id" validate:"required,uuid"`
	SellerID string  `json:"seller_id" validate:"required,uuid"`
	Title    *string `json:"title,omitempty" validate:"omitempty,min=1,max=200"`
	Price    *int64  `json:"price,omitempty" validate:"omitempty,gt=0"`
	Stock    *int64  `json:"stock,omitempty" validate:"omitempty,gte=0"`
}

type DeleteListingRequest struct {
	ID       string `json:"id" validate:"required,uuid"`
	SellerID string `json:"seller_id" validate:"required,uuid"`
}

type ReserveListingRequest struct {
	ReservationID string `json:"reservation_id"`
	ListingID     string `json:"listing_id" validate:"required,uuid"`
	Qty           int64  `json:"qty" validate:"gt=0"`
}

type ListingResponse struct {
	ID        string `json:"id"`
	SellerID  string `json:"seller_id"`
	Title     string `json:"title"`
	Price     int64  `json:"price"`
	Stock     int64  `json:"stock"`
	Sold      int64  `json:"sold"`
	CreatedAt string `json:"created_at"`
}

type GetListingResponse struct {
	Listing ListingResponse `json:"listing"`
	Cache   string          `json:"-"`
}

type ListResponse struct {
	Listings []ListingResponse `json:"items"`
	Total    int64             `json:"total"`
}

type ReserveListingResponse struct {
	Remaining int64 `json:"remaining"`
}

type OrderCreatedEvent struct {
	EventID   string `json:"event_id" validate:"required,uuid"`
	OrderID   string `json:"order_id" validate:"required,uuid"`
	BuyerID   string `json:"buyer_id" validate:"required,uuid"`
	ListingID string `json:"listing_id" validate:"required,uuid"`
	Qty       int64  `json:"qty" validate:"gt=0"`
	CreatedAt string `json:"created_at" validate:"required"`
}
