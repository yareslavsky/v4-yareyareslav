package model

type Listing struct {
	ID        string
	Title     string
	Price     int64
	Stock     int64
	Sold      int64
	SellerID  string
	CreatedAt string
}

type UpdateListing struct {
	ID       string
	SellerID string
	Title    *string
	Price    *int64
	Stock    *int64
}
