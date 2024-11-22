package authenticator

import (
	"context"
	"gw2lfgserver/clientinfo"
	"gw2lfgserver/keyresolver"

	grpc_auth "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Authenticator struct {
	keyResolver *keyresolver.Resolver
}

func New(r *keyresolver.Resolver) *Authenticator {
	return &Authenticator{keyResolver: r}
}

func (a *Authenticator) Authenticate(ctx context.Context) (context.Context, error) {
	token, err := grpc_auth.AuthFromMD(ctx, "bearer")
	if err != nil {
		return nil, err
	}

	clientId, err := a.keyResolver.Resolve(ctx, token)
	if err != nil {
		return nil, err
	}

	if clientId == "" {
		return nil, status.Errorf(codes.Unauthenticated, "invalid auth token")
	}

	return clientinfo.ToContext(ctx, &clientinfo.ClientInfo{
		AccountID: clientId,
		Token:     token,
	}), nil
}
