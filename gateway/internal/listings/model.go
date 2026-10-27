package listings

import "encoding/json"

type Listing struct {
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	Price     json.Number `json:"price"`
	Stock     int64       `json:"stock"`
	Sold      int64       `json:"sold"`
	SellerID  string      `json:"seller_id"`
	CreatedAt string      `json:"created_at"`
}
