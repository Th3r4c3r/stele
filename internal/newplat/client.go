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
	"net/http"
	"time"
)

// BaseURL is the production newplat host. Override via Client.BaseURL
// in tests.
const BaseURL = "https://newplat.vmotosoco-service.com"

// Client talks to newplat. Construct via New.
type Client struct {
	HTTP    *http.Client
	BaseURL string
	token   string
}

// New returns a Client that authenticates with the given token.
// Token is taken by value, not by reference, so rotating it requires
// constructing a new Client (cheap: just an env var read at startup).
func New(token string) *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: 15 * time.Second},
		BaseURL: BaseURL,
		token:   token,
	}
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
// if newplat has no record, ErrTokenInvalid on 401/403.
func (c *Client) FetchVIN(ctx context.Context, vin string) (*Detail, error) {
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
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Authorization", c.token)
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
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("newplat: decode envelope: %w", err)
	}
	if env.Code != 200 && env.Code != 0 {
		// Some newplat error codes also signal expired auth. Treat
		// 401/403/40101 (token related) as ErrTokenInvalid.
		if env.Code == 401 || env.Code == 403 || env.Code == 40101 {
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
