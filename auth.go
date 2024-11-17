package main

import "context"

type clientInfoContextKey struct{}
type clientInfo struct {
	AccountID string
	Token     string
}

func withClientInfo(ctx context.Context, info *clientInfo) context.Context {
	return context.WithValue(ctx, clientInfoContextKey{}, info)
}

func clientInfoFromContext(ctx context.Context) *clientInfo {
	info, ok := ctx.Value(clientInfoContextKey{}).(*clientInfo)
	if ok && info != nil && info.AccountID != "" && info.Token != "" {
		return info
	} else {
		return nil
	}
}
