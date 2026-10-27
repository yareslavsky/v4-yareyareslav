package handler

import (
	"context"
	"marketplace/gen/catalogpb"

	"google.golang.org/protobuf/types/known/emptypb"
)

func (h *GRPCHandler) ReleaseReservation(ctx context.Context, req *catalogpb.ReleaseReservationRequest) (*emptypb.Empty, error) {
	if err := h.service.ReleaseReservation(ctx, req.GetReservationId()); err != nil {
		return nil, toGRPCError(err)
	}
	return &emptypb.Empty{}, nil
}
