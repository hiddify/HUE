package server

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	huev1 "github.com/hiddify/hue/gen/go/hue/v1"
	"github.com/hiddify/hue/internal/auth"
	"github.com/hiddify/hue/internal/ent"
	entrefreshtoken "github.com/hiddify/hue/internal/ent/refreshtoken"
	entreseller "github.com/hiddify/hue/internal/ent/reseller"
	entsubscriber "github.com/hiddify/hue/internal/ent/subscriber"
	"github.com/hiddify/hue/internal/eventstore"
)

// AuthServer implements hue.v1.AuthService — anonymous login flow.
//
// Three login paths:
//   * Subscriber (proto "Client"): username + plaintext password match
//     against AES-GCM-decrypted Subscriber.password_ciphertext.
//   * Reseller: username + password match against Argon2id-hashed
//     Reseller.password_hash.
//   * Owner sudo: if the caller's Authorization header presents a
//     valid OWNER ApiKey, the password is ignored entirely. The
//     resulting JWT carries sudo=true + tgt=<target subject id>; an
//     OWNER_SUDO_LOGIN event is emitted.
//
// Brute-force protection: per (ip, username), 5 failures in 15 min →
// RESOURCE_EXHAUSTED for the rest of the window.
type AuthServer struct {
	huev1.UnimplementedAuthServiceServer

	db         *ent.Client
	authn      *auth.Authenticator
	signingKey *auth.SigningKeyMaterial
	lockout    *auth.Lockout
	events     *eventstore.Store
	logger     *slog.Logger
}

func (s *AuthServer) Login(ctx context.Context, req *huev1.LoginRequest) (*huev1.LoginResponse, error) {
	now := time.Now().UTC()
	ip := clientIPFromCtx(ctx)
	if ok, until := s.lockout.Allowed(ip, req.GetUsername(), now); !ok {
		_ = s.events.Append(ctx, eventstore.Event{
			Type:      "login_locked_out",
			Timestamp: now,
			Metadata:  map[string]any{"ip": ip, "username": req.GetUsername(), "until": until},
		})
		return nil, status.Errorf(codes.ResourceExhausted, "locked out until %s", until.Format(time.RFC3339))
	}

	// Owner sudo path — check API key on the same context.
	if actor, err := s.authn.Authenticate(ctx); err == nil && actor.Kind == auth.PrincipalKindOwner {
		return s.sudoLogin(ctx, actor, req.GetUsername(), now)
	}

	// Reseller path first (admin users), then Subscriber.
	if resp, err := s.tryResellerLogin(ctx, req.GetUsername(), req.GetPassword(), ip, now); err == nil {
		return resp, nil
	}
	resp, err := s.trySubscriberLogin(ctx, req.GetUsername(), req.GetPassword(), ip, now)
	if err != nil {
		locked, until := s.lockout.RecordFailure(ip, req.GetUsername(), now)
		eventType := "login_failed"
		if locked {
			eventType = "login_locked_out"
		}
		_ = s.events.Append(ctx, eventstore.Event{
			Type:      eventType,
			Timestamp: now,
			Metadata:  map[string]any{"ip": ip, "username": req.GetUsername(), "until": until},
		})
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}
	s.lockout.RecordSuccess(ip, req.GetUsername())
	return resp, nil
}

func (s *AuthServer) tryResellerLogin(ctx context.Context, username, password, ip string, now time.Time) (*huev1.LoginResponse, error) {
	row, err := s.db.Reseller.Query().Where(entreseller.Name(username)).Only(ctx)
	if err != nil {
		return nil, err
	}
	if err := auth.VerifyToken(password, row.PasswordHash); err != nil {
		return nil, err
	}
	return s.issueTokens(ctx, "reseller", row.ID, false, uuid.Nil, ip, now)
}

func (s *AuthServer) trySubscriberLogin(ctx context.Context, username, password, ip string, now time.Time) (*huev1.LoginResponse, error) {
	row, err := s.db.Subscriber.Query().Where(entsubscriber.Username(username)).Only(ctx)
	if err != nil {
		return nil, err
	}
	plain, err := auth.Decrypt(row.PasswordCiphertext, row.PasswordKeyID)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(plain, []byte(password)) != 1 {
		return nil, status.Error(codes.Unauthenticated, "bad password")
	}
	return s.issueTokens(ctx, "subscriber", row.ID, false, uuid.Nil, ip, now)
}

func (s *AuthServer) sudoLogin(ctx context.Context, owner auth.Actor, username string, now time.Time) (*huev1.LoginResponse, error) {
	var (
		targetID uuid.UUID
		kind     string
	)
	if r, err := s.db.Reseller.Query().Where(entreseller.Name(username)).Only(ctx); err == nil {
		targetID = r.ID
		kind = "reseller"
	} else if s2, err := s.db.Subscriber.Query().Where(entsubscriber.Username(username)).Only(ctx); err == nil {
		targetID = s2.ID
		kind = "subscriber"
	} else {
		return nil, status.Error(codes.NotFound, "sudo target not found")
	}
	_ = s.events.Append(ctx, eventstore.Event{
		Type:      "owner_sudo_login",
		ClientID:  conditionalID(kind == "subscriber", targetID),
		Timestamp: now,
		Metadata: map[string]any{
			"owner_key_id": owner.KeyID.String(),
			"target_kind":  kind,
			"target_id":    targetID.String(),
		},
	})
	return s.issueTokens(ctx, kind, targetID, true, targetID, clientIPFromCtx(ctx), now)
}

func (s *AuthServer) issueTokens(ctx context.Context, kind string, subjectID uuid.UUID, ownerSudo bool, sudoTargetID uuid.UUID, _ string, now time.Time) (*huev1.LoginResponse, error) {
	jti := uuid.NewString()
	accessExp := now.Add(auth.DefaultAccessTTL())
	claims := auth.Claims{
		Subject:   subjectID.String(),
		Kind:      kind,
		IssuedAt:  now.Unix(),
		ExpiresAt: accessExp.Unix(),
		JTI:       jti,
		OwnerSudo: ownerSudo,
	}
	if sudoTargetID != uuid.Nil {
		claims.SudoTargetID = sudoTargetID.String()
	}
	access, err := auth.SignClaims(s.signingKey, claims)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "sign: %v", err)
	}

	refreshPlain, refreshJTI, err := auth.NewRefreshToken()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "refresh: %v", err)
	}
	refreshExp := now.Add(auth.DefaultRefreshTTL())
	_, err = s.db.RefreshToken.Create().
		SetJti(refreshJTI).
		SetPrincipalKind(refreshPrincipalKind(kind)).
		SetPrincipalID(subjectID.String()).
		SetExpiresAt(refreshExp).
		SetOwnerSudo(ownerSudo).
		Save(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}

	_ = s.events.Append(ctx, eventstore.Event{
		Type:      "login_succeeded",
		Timestamp: now,
		Metadata:  map[string]any{"kind": kind, "subject_id": subjectID.String(), "sudo": ownerSudo},
	})

	var protoKind huev1.PrincipalKind
	switch kind {
	case "subscriber":
		protoKind = huev1.PrincipalKind_PRINCIPAL_KIND_CLIENT
	case "reseller":
		protoKind = huev1.PrincipalKind_PRINCIPAL_KIND_RESELLER
	case "owner":
		protoKind = huev1.PrincipalKind_PRINCIPAL_KIND_OWNER
	}
	return &huev1.LoginResponse{
		AccessToken:  access,
		RefreshToken: refreshPlain,
		Kind:         protoKind,
		SubjectId:    subjectID.String(),
		ExpiresAt:    timestamppb.New(accessExp),
	}, nil
}

func (s *AuthServer) Logout(ctx context.Context, req *huev1.LogoutRequest) (*emptypb.Empty, error) {
	if req.GetRefreshToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "refresh_token is required")
	}
	jti := auth.HashRefresh(req.GetRefreshToken())
	row, err := s.db.RefreshToken.Query().Where(entrefreshtoken.Jti(jti)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return &emptypb.Empty{}, nil
		}
		return nil, mapEntError(err)
	}
	_, err = s.db.RefreshToken.UpdateOneID(row.ID).SetRevokedAt(time.Now().UTC()).Save(ctx)
	if err != nil {
		return nil, mapEntError(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *AuthServer) Refresh(ctx context.Context, req *huev1.RefreshRequest) (*huev1.LoginResponse, error) {
	if req.GetRefreshToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "refresh_token is required")
	}
	now := time.Now().UTC()
	jti := auth.HashRefresh(req.GetRefreshToken())
	row, err := s.db.RefreshToken.Query().Where(entrefreshtoken.Jti(jti)).Only(ctx)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "unknown refresh token")
	}
	if row.RevokedAt != nil || !row.ExpiresAt.After(now) || row.ReplacedBy != "" {
		return nil, status.Error(codes.Unauthenticated, "refresh token invalid")
	}
	subjectID, err := uuid.Parse(row.PrincipalID)
	if err != nil {
		return nil, status.Error(codes.Internal, "stored principal id malformed")
	}
	resp, err := s.issueTokens(ctx, string(row.PrincipalKind), subjectID, row.OwnerSudo, uuid.Nil, "", now)
	if err != nil {
		return nil, err
	}
	// Rotate the old token: mark replaced_by.
	newJTI := auth.HashRefresh(resp.GetRefreshToken())
	_, _ = s.db.RefreshToken.UpdateOneID(row.ID).
		SetRevokedAt(now).
		SetReplacedBy(newJTI).
		Save(ctx)
	return resp, nil
}

func (s *AuthServer) ChangePassword(ctx context.Context, req *huev1.ChangePasswordRequest) (*emptypb.Empty, error) {
	actor, ok := auth.FromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "must be logged in")
	}
	if req.GetNewPassword() == "" {
		return nil, status.Error(codes.InvalidArgument, "new_password is required")
	}
	switch actor.Kind {
	case auth.PrincipalKindReseller:
		row, err := s.db.Reseller.Get(ctx, actor.SubjectID)
		if err != nil {
			return nil, mapEntError(err)
		}
		if err := auth.VerifyToken(req.GetCurrentPassword(), row.PasswordHash); err != nil {
			return nil, status.Error(codes.Unauthenticated, "current password mismatch")
		}
		hash, err := auth.HashToken(req.GetNewPassword())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "hash: %v", err)
		}
		_, err = s.db.Reseller.UpdateOneID(actor.SubjectID).SetPasswordHash(hash).Save(ctx)
		return &emptypb.Empty{}, mapEntError(err)

	case auth.PrincipalKindSubscriber:
		row, err := s.db.Subscriber.Get(ctx, actor.SubjectID)
		if err != nil {
			return nil, mapEntError(err)
		}
		plain, err := auth.Decrypt(row.PasswordCiphertext, row.PasswordKeyID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "decrypt: %v", err)
		}
		if subtle.ConstantTimeCompare(plain, []byte(req.GetCurrentPassword())) != 1 {
			return nil, status.Error(codes.Unauthenticated, "current password mismatch")
		}
		ct, keyID, err := auth.Encrypt([]byte(req.GetNewPassword()))
		if err != nil {
			return nil, status.Errorf(codes.Internal, "encrypt: %v", err)
		}
		_, err = s.db.Subscriber.UpdateOneID(actor.SubjectID).
			SetPasswordCiphertext(ct).
			SetPasswordKeyID(keyID).
			Save(ctx)
		return &emptypb.Empty{}, mapEntError(err)
	}
	return nil, status.Error(codes.PermissionDenied, "principal kind cannot change password")
}

// helpers

func refreshPrincipalKind(s string) entrefreshtoken.PrincipalKind {
	switch s {
	case "subscriber":
		return entrefreshtoken.PrincipalKindClient
	case "reseller":
		return entrefreshtoken.PrincipalKindReseller
	}
	return entrefreshtoken.PrincipalKindOwner
}

func clientIPFromCtx(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p == nil {
		return ""
	}
	return p.Addr.String()
}

func conditionalID(yes bool, id uuid.UUID) string {
	if yes {
		return id.String()
	}
	return ""
}
