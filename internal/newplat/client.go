// Package newplat is a thin HTTP client for the Vmoto `newplat` SaaS
// (https://newplat.vmotosoco-service.com). It exposes the subset of
// fields Stele's telemetry view needs and the raw JSON payload for
// future projections (see ADR-014).
//
// Auth is a JWT passed in the Authorization header. The token is a
// credential: callers MUST source it from STELE_NEWPLAT_TOKEN env
// var. This package never logs the token verbatim.
package newplat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// BaseURL is the production newplat host. Override via Client.BaseURL
// in tests.
const BaseURL = "https://newplat.vmotosoco-service.com"

// Client talks to newplat. Construct via New (static token) or
// NewWithCredentials (auto-refresh on 401/403).
type Client struct {
	HTTP    *http.Client
	BaseURL string

	mu    sync.RWMutex
	token string

	// creds set => auto-refresh is enabled. ErrTokenInvalid triggers
	// one Login attempt followed by a retry. Empty => static token,
	// no refresh, ErrTokenInvalid bubbles to the caller.
	creds *Credentials
}

// Credentials are the newplat login form fields. customerName is
// usually the human-readable customer (e.g. "VMOTO"); customerID
// is the numeric tenant header (almost always "1" for Vmoto today).
type Credentials struct {
	CustomerName string
	CustomerID   string // header `customerid`; defaults to "1"
	UserAccount  string
	UserPassword string // sent verbatim; newplat hashes server-side
}

// New returns a Client with a static token. No auto-refresh: an
// expired token surfaces as ErrTokenInvalid to the caller.
func New(token string) *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: 15 * time.Second},
		BaseURL: BaseURL,
		token:   token,
	}
}

// NewWithCredentials returns a Client that performs a Login at first
// use and automatically refreshes the token on ErrTokenInvalid. The
// initial token can be empty (then Login is called lazily on first
// FetchVIN) or pre-seeded to skip the first round-trip.
func NewWithCredentials(initialToken string, creds Credentials) *Client {
	if creds.CustomerID == "" {
		creds.CustomerID = "1"
	}
	return &Client{
		HTTP:    &http.Client{Timeout: 15 * time.Second},
		BaseURL: BaseURL,
		token:   initialToken,
		creds:   &creds,
	}
}

// Token returns the current bearer token. Useful for diagnostics; do
// not log the result.
func (c *Client) Token() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// Login posts the credentials to /v1/plat/login/loginByAccount and
// stores the resulting JWT. Safe to call any time; concurrent calls
// serialize on the mutex (no thundering-herd refresh).
func (c *Client) Login(ctx context.Context) (string, error) {
	if c.creds == nil {
		return "", errors.New("newplat: no credentials configured (Login requires NewWithCredentials)")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	body, err := json.Marshal(map[string]string{
		"customerName": c.creds.CustomerName,
		"userAccount":  c.creds.UserAccount,
		"userPassword": c.creds.UserPassword,
	})
	if err != nil {
		return "", fmt.Errorf("newplat.Login marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/v1/plat/login/loginByAccount", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("newplat.Login new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("customerid", c.creds.CustomerID)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("newplat.Login do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("newplat.Login http %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("newplat.Login read: %w", err)
	}
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("newplat.Login decode: %w", err)
	}
	if env.Code != 200 || !env.Success || env.Data.Token == "" {
		return "", fmt.Errorf("newplat.Login rejected: code=%d msg=%q", env.Code, env.Msg)
	}
	c.token = env.Data.Token
	slog.Info("newplat token refreshed", "account", c.creds.UserAccount)
	return env.Data.Token, nil
}

// ErrTokenInvalid signals that newplat rejected our credential. The
// caller surfaces this to the UI with the "refresh STELE_NEWPLAT_TOKEN"
// message; sync proceeds for other VINs but this one is parked.
var ErrTokenInvalid = errors.New("newplat: token invalid or expired")

// ErrNotFound is returned when a VIN does not exist on newplat.
var ErrNotFound = errors.New("newplat: VIN not found")

// Detail is the subset of newplat's getCarBaseInfoDetail response
// that Stele actually projects into vehicle_telemetry. Fields we do
// not currently use stay in RawPayload as a json.RawMessage; the
// projector can pull them out later without re-fetching.
type Detail struct {
	VIN         string
	IsOnline    bool
	CarBaseInfo CarBaseInfo
	Device      *Device
	Pojo        *Pojo
	RawPayload  json.RawMessage
}

// CarBaseInfo mirrors the carBaseInfo node.
type CarBaseInfo struct {
	CarFrameNumber string  `json:"carFrameNumber"`
	DeviceNo       string  `json:"deviceNo"`
	CarMotorNumber string  `json:"carMotorNumber"`
	CarModelName   string  `json:"carModelName"`
	TotalMileage   float64 `json:"totalMileage"`
}

// Device mirrors the device node (SIM + modem). Pointer in Detail so
// "device: null" stays expressible.
type Device struct {
	DeviceNo           string `json:"deviceNo"`
	ICCID              string `json:"iccid"`
	ICCIDState         int    `json:"iccidState"`
	SimStartTime       string `json:"simStartTime"`
	SimEndTime         string `json:"simEndTime"`
	AgreementStartTime string `json:"agreementStartTime"`
	AgreementEndTime   string `json:"agreementEndTime"`
}

// Pojo mirrors the pojo node (live telemetry from the bike).
type Pojo struct {
	NowElec        int     `json:"nowElec"`
	EcuElec        int     `json:"ecuElec"`
	Endurance      int     `json:"endurance"`
	Gsm            int     `json:"gsm"`
	Gps            int     `json:"gps"`
	Latitude       float64 `json:"latitude"`
	Longitude      float64 `json:"longitude"`
	LoginTime      string  `json:"loginTime"`
	LastGpsTime    string  `json:"lastGpsTime"`
	BmsTemperature int     `json:"bmsTemperature"`
	FotaVer        string  `json:"fota_ver"`
}

// FetchVIN queries getCarBaseInfoDetail for vin. Returns ErrNotFound
// if newplat has no record. When credentials are configured the
// client transparently re-Logins once on ErrTokenInvalid; without
// credentials the same error bubbles up to the caller.
func (c *Client) FetchVIN(ctx context.Context, vin string) (*Detail, error) {
	d, err := c.fetchOnce(ctx, vin)
	if !errors.Is(err, ErrTokenInvalid) || c.creds == nil {
		return d, err
	}
	// One re-login attempt. If Login itself fails we surface the
	// original ErrTokenInvalid (clearer signal to the caller than
	// "login http 500"). Successful login → one retry.
	if _, lerr := c.Login(ctx); lerr != nil {
		slog.Error("newplat auto-refresh failed", "err", lerr)
		return nil, ErrTokenInvalid
	}
	return c.fetchOnce(ctx, vin)
}

// fetchOnce is FetchVIN minus the auto-refresh layer. Pulled out so
// the retry path is a simple re-call instead of a flag-juggling
// recursion.
func (c *Client) fetchOnce(ctx context.Context, vin string) (*Detail, error) {
	body, err := json.Marshal(map[string]string{"carFrameNumber": vin})
	if err != nil {
		return nil, fmt.Errorf("newplat: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/v1/plat/car_base_info/getCarBaseInfoDetail",
		bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("newplat: new request: %w", err)
	}
	c.mu.RLock()
	tok := c.token
	c.mu.RUnlock()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Authorization", tok)
	req.Header.Set("customerId", "1") // Vmoto tenant; identical to vmoto-ops tool.

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("newplat: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrTokenInvalid
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("newplat: http %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("newplat: read body: %w", err)
	}

	// Newplat wraps every response in {code, msg, data}. data may be
	// null for an unknown VIN (we treat that as ErrNotFound).
	var env struct {
		Code    int             `json:"code"`
		Msg     string          `json:"msg"`
		Message string          `json:"message"`
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("newplat: decode envelope: %w", err)
	}
	if env.Code != 200 && env.Code != 0 {
		// Newplat does not use HTTP 401/403 for auth errors: a bad or
		// expired token comes back as HTTP 200 with envelope
		// {code: 500, success: false, message: "系统错误"}. The
		// 401/403/40101 codes appear in older docs but in practice
		// every auth failure observed so far maps to code=500 +
		// success=false. We classify them all as ErrTokenInvalid so
		// the auto-refresh path (FetchVIN wrapper) re-Logins once.
		// A real server-side 500 will fail the retry too and surface
		// here as the original error.
		if env.Code == 401 || env.Code == 403 || env.Code == 40101 || (env.Code == 500 && !env.Success) {
			return nil, ErrTokenInvalid
		}
		return nil, fmt.Errorf("newplat: api error %d: %s", env.Code, env.Msg)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil, ErrNotFound
	}

	// Decode the typed fields we project, keep the raw data node for
	// forensics + future projections.
	var d struct {
		IsOnline    bool        `json:"isOnline"`
		CarBaseInfo CarBaseInfo `json:"carBaseInfo"`
		Device      *Device     `json:"device"`
		Pojo        *Pojo       `json:"pojo"`
	}
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return nil, fmt.Errorf("newplat: decode detail: %w", err)
	}
	return &Detail{
		VIN:         vin,
		IsOnline:    d.IsOnline,
		CarBaseInfo: d.CarBaseInfo,
		Device:      d.Device,
		Pojo:        d.Pojo,
		RawPayload:  env.Data,
	}, nil
}

// ParseNewplatTime parses newplat's `2006-01-02 15:04:05` UTC-naive
// timestamps. Returns the zero time on empty/invalid input (caller
// stores NULL for the zero case).
func ParseNewplatTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		return time.Time{}
	}
	return t
}
