package kpme

import (
	"encoding/json"
	"fmt"
	"gw2lfgserver/pb"
	"io"
	"net/http"
	"sync"
	"time"
)

type cacheEntry struct {
	response   *KillProofResponse
	createTime time.Time
}

type Client struct {
	cache         map[string]cacheEntry
	mu            sync.RWMutex
	cacheEntryTtl time.Duration
}

func NewClient() *Client {
	return &Client{
		cache:         make(map[string]cacheEntry),
		cacheEntryTtl: 5 * time.Minute,
	}
}

type KillProofResponse struct {
	LastRefresh        string          `json:"last_refresh"`
	NextRefresh        string          `json:"next_refresh"`
	NextRefreshSeconds int             `json:"next_refresh_seconds"`
	OriginalUCE        UCE             `json:"original_uce"`
	ValidAPIKey        bool            `json:"valid_api_key"`
	KPID               string          `json:"kpid"`
	Linked             []LinkedAccount `json:"linked"`
	LinkedTotals       LinkedTotals    `json:"linked_totals"`
	Coffers            []Item          `json:"coffers"`
	Tokens             []Item          `json:"tokens"`
	Titles             []Title         `json:"titles"`
	ProofURL           string          `json:"proof_url"`
	AccountName        string          `json:"account_name"`
	Killproofs         []Item          `json:"killproofs"`
}

type UCE struct {
	AtDate string `json:"at_date"`
	Amount int    `json:"amount"`
}

type LinkedAccount struct {
	Killproofs         []Item  `json:"killproofs"`
	AccountName        string  `json:"account_name"`
	KPID               string  `json:"kpid"`
	ValidAPIKey        bool    `json:"valid_api_key"`
	Tokens             []Item  `json:"tokens"`
	Titles             []Title `json:"titles"`
	ProofURL           string  `json:"proof_url"`
	OriginalUCE        UCE     `json:"original_uce"`
	NextRefreshSeconds int     `json:"next_refresh_seconds"`
	LastRefresh        string  `json:"last_refresh"`
	NextRefresh        string  `json:"next_refresh"`
	Coffers            []Item  `json:"coffers"`
}

type LinkedTotals struct {
	Coffers    []Item  `json:"coffers"`
	Killproofs []Item  `json:"killproofs"`
	Titles     []Title `json:"titles"`
	Tokens     []Item  `json:"tokens"`
}

type Item struct {
	Amount int    `json:"amount"`
	Name   string `json:"name"`
	ID     int    `json:"id"`
}

type Title struct {
	Mode string `json:"mode"`
	Name string `json:"name"`
	ID   int    `json:"id"`
}

func (c *Client) GetKillProof(accountName string) (*KillProofResponse, error) {
	if kpResp := c.getCached(accountName); kpResp != nil {
		return kpResp, nil
	}

	kpResp, err := c.fetch(accountName)
	if err != nil {
		return nil, err
	}

	c.cacheResult(accountName, kpResp)

	return kpResp, nil
}

func (c *Client) getCached(accountName string) *KillProofResponse {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.cache[accountName]
	if !ok || time.Since(entry.createTime) > c.cacheEntryTtl {
		delete(c.cache, accountName)
		return nil
	}

	return entry.response
}

func (c *Client) cacheResult(accountName string, kpResp *KillProofResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache[accountName] = cacheEntry{
		response:   kpResp,
		createTime: time.Now(),
	}
}

func (c *Client) fetch(accountName string) (*KillProofResponse, error) {
	resp, err := http.DefaultClient.Get("https://killproof.me/api/kp/" + accountName + "?lang=en")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch killproof data: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var kpResp KillProofResponse
	if err := json.Unmarshal(body, &kpResp); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w", err)
	}

	return &kpResp, nil
}

func KillProofReponseToProto(kp *KillProofResponse) *pb.KillProof {
	li := 0
	bskp := 0
	ufe := 0
	w1 := 0
	w2 := 0
	w3 := 0
	w4 := 0
	w5 := 0
	w6 := 0
	w7 := 0
	w8 := 0

	// Sum up the killproofs and tokens for the main account
	for _, kpItem := range kp.Killproofs {
		switch kpItem.Name {
		case "Legendary Insight", "Legendary Divination":
			li += kpItem.Amount
		case "Boneskinner Ritual Vial":
			bskp += kpItem.Amount
		case "Unstable Cosmic Essence":
			ufe += kpItem.Amount
		}
	}

	for _, token := range kp.Tokens {
		switch token.Name {
		case "Sabetha Flamethrower Fragment Piece", "Sabetha's Coffer":
			w1 += token.Amount
		case "White Mantle Abomination Crystal", "Matthias's Coffer":
			w2 += token.Amount
		case "Ribbon Scrap", "Xera's Coffer":
			w3 += token.Amount
		case "Fragment of Saul's Burden", "Deimos's Coffer":
			w4 += token.Amount
		case "Dhuum's Token", "Dhuum's Coffer":
			w5 += token.Amount
		case "Qadim's Token", "Qadim's Coffer":
			w6 += token.Amount
		case "Ether Djinn's Token", "Qadim the Peerless's Coffer":
			w7 += token.Amount
		case "Ura's Token", "Ura's Coffer":
			w8 += token.Amount
		}
	}

	// Sum up the killproofs and tokens for linked accounts
	for _, linked := range kp.Linked {
		for _, kpItem := range linked.Killproofs {
			switch kpItem.Name {
			case "Legendary Insight", "Legendary Divination":
				li += kpItem.Amount
			case "Boneskinner Ritual Vial":
				bskp += kpItem.Amount
			case "Unstable Cosmic Essence":
				ufe += kpItem.Amount
			}
		}

		for _, token := range linked.Tokens {
			switch token.Name {
			case "Sabetha Flamethrower Fragment Piece", "Sabetha's Coffer":
				w1 += token.Amount
			case "White Mantle Abomination Crystal", "Matthias's Coffer":
				w2 += token.Amount
			case "Ribbon Scrap", "Xera's Coffer":
				w3 += token.Amount
			case "Fragment of Saul's Burden", "Deimos's Coffer":
				w4 += token.Amount
			case "Dhuum's Token", "Dhuum's Coffer":
				w5 += token.Amount
			case "Qadim's Token", "Qadim's Coffer":
				w6 += token.Amount
			case "Ether Djinn's Token", "Qadim the Peerless's Coffer":
				w7 += token.Amount
			case "Ura's Token", "Ura's Coffer":
				w8 += token.Amount
			}
		}
	}

	return &pb.KillProof{
		Li:   int32(li),
		Bskp: int32(bskp),
		Ufe:  int32(ufe),
		W1:   int32(w1),
		W2:   int32(w2),
		W3:   int32(w3),
		W4:   int32(w4),
		W5:   int32(w5),
		W6:   int32(w6),
		W7:   int32(w7),
		W8:   int32(w8),
	}
}
