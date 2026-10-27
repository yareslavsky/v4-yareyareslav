package mapper

import (
	"marketplace/catalog-svc/internal/listings/dto"
	"marketplace/catalog-svc/internal/listings/model"
)

func ToListingResponse(listing model.Listing) dto.ListingResponse {
	return dto.ListingResponse{
		ID:        listing.ID,
		SellerID:  listing.SellerID,
		Title:     listing.Title,
		Price:     listing.Price,
		Stock:     listing.Stock,
		Sold:      listing.Sold,
		CreatedAt: listing.CreatedAt,
	}
}
