package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type keyResolver struct{}

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

func (k *keyResolver) Resolve(ctx context.Context, key string) (string, error) {
	accountRes, err := http.DefaultClient.Get("https://api.guildwars2.com/v2/account?access_token=" + key)
	if err != nil {
		return "", status.Error(codes.Internal, fmt.Errorf("failed to resolve key: %w", err).Error())
	}
	defer accountRes.Body.Close()

	var account account
	if err = json.NewDecoder(accountRes.Body).Decode(&account); err != nil {
		return "", status.Error(codes.Internal, fmt.Errorf("failed to decode account: %w", err).Error())
	}
	return account.Name, nil
}
