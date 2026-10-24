package base

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// TransferTopic is keccak256("Transfer(address,address,uint256)").
const TransferTopic = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

// Client is a minimal JSON-RPC client; it needs only a handful of calls, so
// it avoids pulling in a full Ethereum library.
type Client struct {
	URL  string
	HTTP *http.Client
}

func NewClient(url string) *Client {
	return &Client{URL: url, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) call(ctx context.Context, method string, out any, params ...any) error {
	body, err := json.Marshal(rpcReq{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
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
		return fmt.Errorf("rpc %s: http %d", method, res.StatusCode)
	}
	var r rpcResp
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		return fmt.Errorf("rpc %s: %w", method, err)
	}
	if r.Error != nil {
		return fmt.Errorf("rpc %s: %s", method, r.Error.Message)
	}
	return json.Unmarshal(r.Result, out)
}

func (c *Client) ChainID(ctx context.Context) (int64, error) {
	var s string
	if err := c.call(ctx, "eth_chainId", &s); err != nil {
		return 0, err
	}
	n, err := ParseQuantity(s)
	if err != nil {
		return 0, err
	}
	return n.Int64(), nil
}

func (c *Client) BlockNumber(ctx context.Context) (int64, error) {
	var s string
	if err := c.call(ctx, "eth_blockNumber", &s); err != nil {
		return 0, err
	}
	n, err := ParseQuantity(s)
	if err != nil {
		return 0, err
	}
	return n.Int64(), nil
}

// BalanceAt is the ETH balance in wei at a block.
func (c *Client) BalanceAt(ctx context.Context, addr string, block int64) (*big.Int, error) {
	var s string
	if err := c.call(ctx, "eth_getBalance", &s, addr, Quantity(block)); err != nil {
		return nil, err
	}
	return ParseQuantity(s)
}

// Call runs eth_call at the latest block and returns the raw return data.
func (c *Client) Call(ctx context.Context, to string, data []byte) ([]byte, error) {
	var s string
	msg := map[string]string{"to": to, "data": "0x" + hex.EncodeToString(data)}
	if err := c.call(ctx, "eth_call", &s, msg, "latest"); err != nil {
		return nil, err
	}
	return hex.DecodeString(strings.TrimPrefix(s, "0x"))
}

// Log is the part of an eth_getLogs entry that deposits need.
type Log struct {
	Address     string   `json:"address"`
	Topics      []string `json:"topics"`
	Data        string   `json:"data"`
	BlockNumber string   `json:"blockNumber"`
	TxHash      string   `json:"transactionHash"`
	LogIndex    string   `json:"logIndex"`
	Removed     bool     `json:"removed"`
}

// TransferLogs returns token Transfer events to any of the recipients in
// [from, to].
func (c *Client) TransferLogs(ctx context.Context, token string, recipients []string, from, to int64) ([]Log, error) {
	padded := make([]string, len(recipients))
	for i, r := range recipients {
		padded[i] = "0x" + strings.Repeat("0", 24) + strings.ToLower(strings.TrimPrefix(r, "0x"))
	}
	filter := map[string]any{
		"address":   token,
		"fromBlock": Quantity(from),
		"toBlock":   Quantity(to),
		"topics":    []any{TransferTopic, nil, padded},
	}
	var logs []Log
	if err := c.call(ctx, "eth_getLogs", &logs, filter); err != nil {
		return nil, err
	}
	return logs, nil
}

// Quantity formats a block number as a JSON-RPC hex quantity.
func Quantity(n int64) string { return fmt.Sprintf("0x%x", n) }

// ParseQuantity parses a JSON-RPC hex quantity of any size.
func ParseQuantity(s string) (*big.Int, error) {
	h := strings.TrimPrefix(s, "0x")
	if h == "" {
		return nil, errors.New("empty quantity")
	}
	n, ok := new(big.Int).SetString(h, 16)
	if !ok {
		return nil, fmt.Errorf("bad quantity %q", s)
	}
	return n, nil
}
