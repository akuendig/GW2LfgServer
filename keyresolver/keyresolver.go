package keyresolver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type cacheEntry struct {
	name       string
	err        error
	createTime time.Time
}

type Resolver struct {
	cache map[string]cacheEntry
	mu    sync.RWMutex
}

type account struct {
	Id           string    `json:"id"`
	Age          int       `json:"age"`
	Name         string    `json:"name"`
	World        int       `json:"world"`
	Guilds       []string  `json:"guilds"`
	GuildLeader  *[]string `json:"guild_leader,omitempty"` // Requires additional guilds scope
	Created      string    `json:"created"`
	Access       []string  `json:"access"`
	Commander    bool      `json:"commander"`
	FractalLevel *int      `json:"fractal_level,omitempty"` // Requires additional progression scope
	DailyAP      *int      `json:"daily_ap,omitempty"`      // Requires additional progression scope
	MonthlyAP    *int      `json:"monthly_ap,omitempty"`    // Requires additional progression scope
	WvwRank      *int      `json:"wvw_rank,omitempty"`      // Requires additional progression scope
	Wvw          *struct {
		TeamID int  `json:"team_id"`
		Rank   *int `json:"rank,omitempty"` // Requires additional progression scope
	} `json:"wvw,omitempty"` // Only present with specific schema versions
	LastModified      *string `json:"last_modified,omitempty"`       // Only present with specific schema versions
	BuildStorageSlots *int    `json:"build_storage_slots,omitempty"` // Requires additional builds scope
}

func New() *Resolver {
	return &Resolver{
		cache: make(map[string]cacheEntry),
	}
}

func (r *Resolver) Resolve(ctx context.Context, key string) (string, error) {
	start := time.Now()
	if name, err := r.getCached(key); err != nil {
		defer func() {
			slog.InfoContext(ctx, "keyresolver.Resolve.CachedError", slog.Duration("elapsed", time.Since(start)))
		}()
		return "", status.Error(codes.Internal, fmt.Errorf("failed to get cached key: %w", err).Error())
	} else if name != nil {
		defer func() {
			slog.InfoContext(ctx, "keyresolver.Resolve.CachedName", slog.Duration("elapsed", time.Since(start)))
		}()
		return *name, nil
	}
	defer func() {
		slog.InfoContext(ctx, "keyresolver.Resolve.Unchached", slog.Duration("elapsed", time.Since(start)))
	}()

	accountName, err := r.fetch(ctx, key)
	if err != nil {
		r.cacheResult(key, "", err)
		return "", status.Error(codes.Internal, fmt.Errorf("failed to decode account: %w", err).Error())
	} else {
		r.cacheResult(key, accountName, nil)
		return accountName, nil
	}
}

func (r *Resolver) getCached(key string) (*string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.cache[key]
	if !ok {
		return nil, nil
	}
	if time.Since(entry.createTime) > time.Hour {
		delete(r.cache, key)
		return nil, nil
	}
	if entry.err != nil {
		return nil, entry.err
	}
	return &entry.name, nil
}

func (r *Resolver) cacheResult(key, name string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.cache[key] = cacheEntry{
		name:       name,
		err:        err,
		createTime: time.Now(),
	}
}

func (r *Resolver) fetch(ctx context.Context, key string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.guildwars2.com/v2/account?access_token="+key, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	accountRes, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to resolve key: %w", err)
	}
	defer accountRes.Body.Close()

	var account account
	if err = json.NewDecoder(accountRes.Body).Decode(&account); err != nil {
		return "", fmt.Errorf("failed to decode account: %w", err)
	}
	return account.Name, nil
}
