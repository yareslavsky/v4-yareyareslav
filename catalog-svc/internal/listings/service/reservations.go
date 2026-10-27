package service

import "context"

func (s *CatalogService) ReleaseReservation(ctx context.Context, id string) error {
	if !isValidUUID(id) {
		return &ValidationError{Fields: []FieldError{{Field: "reservation_id", Message: "must be a valid UUID"}}}
	}
	listingID, err := s.repository.ReleaseReservation(ctx, id)
	if err != nil {
		return err
	}
	if listingID != "" {
		s.invalidateCache(ctx, listingID)
	}
	return nil
}
