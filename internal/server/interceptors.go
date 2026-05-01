package server

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"buf.build/go/protovalidate"
)

// PanicRecovery converts panics into Internal errors and logs the stack.
// Without this, a panic in any handler kills the server-wide goroutine.
func PanicRecovery(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.ErrorContext(ctx, "panic in handler",
					"method", info.FullMethod,
					"panic", r,
					"stack", string(debug.Stack()),
				)
				err = status.Errorf(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}

// PanicRecoveryStream is the streaming counterpart.
func PanicRecoveryStream(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.ErrorContext(ss.Context(), "panic in stream handler",
					"method", info.FullMethod,
					"panic", r,
					"stack", string(debug.Stack()),
				)
				err = status.Errorf(codes.Internal, "internal error")
			}
		}()
		return handler(srv, ss)
	}
}

// SlogUnary logs every request with method, duration, and code. Body is
// not logged — use a separate debug-only logger if you need that.
func SlogUnary(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		level := slog.LevelInfo
		if err != nil {
			level = slog.LevelWarn
		}
		logger.LogAttrs(ctx, level, "rpc",
			slog.String("method", info.FullMethod),
			slog.Duration("dur", time.Since(start)),
			slog.String("code", status.Code(err).String()),
		)
		return resp, err
	}
}

// Validate runs protovalidate-go (CEL rules from the proto file) against
// every inbound message. This keeps validation declarative and consistent
// across gRPC and gateway-translated paths — no handwritten guards.
func Validate(v protovalidate.Validator) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if msg, ok := req.(proto.Message); ok {
			if err := v.Validate(msg); err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "validation: %v", err)
			}
		}
		return handler(ctx, req)
	}
}
