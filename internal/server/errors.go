package server

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/hiddify/hue/internal/ent"
)

// mapEntError turns ent's error sentinels into appropriate gRPC codes.
func mapEntError(err error) error {
	switch {
	case err == nil:
		return nil
	case ent.IsNotFound(err):
		return status.Error(codes.NotFound, err.Error())
	case ent.IsConstraintError(err):
		return status.Error(codes.AlreadyExists, err.Error())
	case ent.IsValidationError(err):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}
