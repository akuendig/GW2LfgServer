package clientinfo

import "context"

type clientInfoContextKey struct{}
type ClientInfo struct {
	AccountID string
	Token     string
}

func ToContext(ctx context.Context, info *ClientInfo) context.Context {
	return context.WithValue(ctx, clientInfoContextKey{}, info)
}

func FromContext(ctx context.Context) *ClientInfo {
	info, ok := ctx.Value(clientInfoContextKey{}).(*ClientInfo)
	if ok && info != nil && info.AccountID != "" && info.Token != "" {
		return info
	} else {
		return nil
	}
}
