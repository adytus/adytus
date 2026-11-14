// Package near reads the NEAR chain over JSON-RPC: enough to follow the
// deposit contract and price NEAR, without a full SDK. Everything is read at
// "final" finality, which NEAR reaches in a couple of seconds and never
// reverts, so there are no confirmations to count.
package near

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// YoctoPerNEAR is 10^24: NEAR amounts are integers of yoctoNEAR.
var YoctoPerNEAR = new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil)

// accountID is NEAR's account ID rule: 2 to 64 characters of lower-case
// parts joined by '.', each part split by single '-' or '_'.
var accountID = regexp.MustCompile(`^(([a-z\d]+[-_])*[a-z\d]+\.)*([a-z\d]+[-_])*[a-z\d]+$`)

// ValidAccountID reports whether s is a well-formed NEAR account ID.
func ValidAccountID(s string) bool {
	return len(s) >= 2 && len(s) <= 64 && accountID.MatchString(s)
}

type Client struct {
	URL  string
	HTTP *http.Client
}

func NewClient(url string) *Client {
	return &Client{URL: url, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Name    string          `json:"name"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
		Cause   *struct {
			Name string          `json:"name"`
			Info json.RawMessage `json:"info"`
		} `json:"cause"`
	} `json:"error"`
}

func (c *Client) call(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "adytus", "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("near rpc %s: http %d", method, res.StatusCode)
	}
	var r rpcResp
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		return fmt.Errorf("near rpc %s: %w", method, err)
	}
	if e := r.Error; e != nil {
		msg := e.Message
		if e.Cause != nil && e.Cause.Name != "" {
			msg = e.Cause.Name
			if len(e.Cause.Info) > 0 && string(e.Cause.Info) != "{}" && string(e.Cause.Info) != "null" {
				msg += " " + string(e.Cause.Info)
			}
		} else if len(e.Data) > 0 {
			msg += ": " + strings.Trim(string(e.Data), `"`)
		}
		return fmt.Errorf("near rpc %s: %s", method, msg)
	}
	// Older nodes put a failed view call in the result instead.
	var inline struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(r.Result, &inline) == nil && inline.Error != "" {
		return fmt.Errorf("near rpc %s: %s", method, inline.Error)
	}
	return json.Unmarshal(r.Result, out)
}

// ChainID is the network the node serves: "mainnet" or "testnet".
func (c *Client) ChainID(ctx context.Context) (string, error) {
	var s struct {
		ChainID string `json:"chain_id"`
	}
	if err := c.call(ctx, "status", []any{}, &s); err != nil {
		return "", err
	}
	return s.ChainID, nil
}

// View calls a contract's view method with JSON args and decodes its JSON
// result into out.
func (c *Client) View(ctx context.Context, contract, method string, args, out any) error {
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	// result is an array of byte values; []byte would expect base64.
	var rr struct {
		Result []int `json:"result"`
	}
	if err := c.call(ctx, "query", map[string]any{
		"request_type": "call_function",
		"finality":     "final",
		"account_id":   contract,
		"method_name":  method,
		"args_base64":  base64.StdEncoding.EncodeToString(raw),
	}, &rr); err != nil {
		return err
	}
	buf := make([]byte, len(rr.Result))
	for i, b := range rr.Result {
		if b < 0 || b > 255 {
			return errors.New("near rpc: bad view result")
		}
		buf[i] = byte(b)
	}
	if err := json.Unmarshal(buf, out); err != nil {
		return fmt.Errorf("%s.%s answered %q: %w", contract, method, truncate(string(buf), 120), err)
	}
	return nil
}

// Balance is an account's spendable NEAR, in yoctoNEAR. Part of a contract's
// balance is held for its storage and can't be sent; see StorageUsage.
func (c *Client) Balance(ctx context.Context, account string) (amount *big.Int, storageBytes int64, err error) {
	var a struct {
		Amount       string `json:"amount"`
		StorageUsage int64  `json:"storage_usage"`
	}
	if err := c.call(ctx, "query", map[string]any{
		"request_type": "view_account",
		"finality":     "final",
		"account_id":   account,
	}, &a); err != nil {
		return nil, 0, err
	}
	n, ok := new(big.Int).SetString(a.Amount, 10)
	if !ok {
		return nil, 0, fmt.Errorf("near rpc: bad balance %q", a.Amount)
	}
	return n, a.StorageUsage, nil
}

// TokenBalance is owner's balance of a NEP-141 token, in its base units.
func (c *Client) TokenBalance(ctx context.Context, token, owner string) (*big.Int, error) {
	var s string
	if err := c.View(ctx, token, "ft_balance_of", map[string]string{"account_id": owner}, &s); err != nil {
		return nil, err
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("ft_balance_of answered %q", s)
	}
	return n, nil
}

// TokenRegistered reports whether account can receive token, which NEP-145
// requires a storage deposit for. A deposit contract that isn't registered
// with USDC makes every USDC payment bounce.
func (c *Client) TokenRegistered(ctx context.Context, token, account string) (bool, error) {
	var bal *struct {
		Total string `json:"total"`
	}
	if err := c.View(ctx, token, "storage_balance_of", map[string]string{"account_id": account}, &bal); err != nil {
		return false, err
	}
	return bal != nil, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
