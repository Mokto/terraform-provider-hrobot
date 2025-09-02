package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
)

type Client struct {
	base string
	user string
	pass string
	http *http.Client
}

func New(base, user, pass string, httpClient *http.Client) *Client {
	return &Client{base: base, user: user, pass: pass, http: httpClient}
}

func (c *Client) do(method, path string, form url.Values, oks ...int) ([]byte, error) {
	var body io.Reader
	if form != nil {
		body = bytes.NewBufferString(form.Encode())
	}
	fmt.Println(method, c.base+path, body)
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.pass)
	fmt.Println(c.user, "password")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	ok := false
	for _, s := range oks {
		if s == resp.StatusCode {
			ok = true
			break
		}
	}
	if !ok {
		log.Printf("API request failed with status %d, body: %s", resp.StatusCode, string(b))
		var ae apiErr
		if err := json.Unmarshal(b, &ae); err == nil && ae.Error.Message != "" {
			return nil, fmt.Errorf("robot: %s: %s", ae.Error.Code, ae.Error.Message)
		}
		return nil, fmt.Errorf("robot: unexpected %d: %s", resp.StatusCode, string(b))
	}
	return b, nil
}

// --- Order

type OrderParams struct {
	ProductID                string
	Dist, Location, Password *string
	Keys, Addons             []string
	Test                     bool
}

func (c *Client) OrderServer(p OrderParams) (*Transaction, error) {
	f := url.Values{}
	f.Set("product_id", p.ProductID)
	if p.Dist != nil {
		f.Set("dist", *p.Dist)
	}
	if p.Location != nil {
		f.Set("location", *p.Location)
	}
	if p.Password != nil {
		f.Set("password", *p.Password)
	}
	for _, k := range p.Keys {
		f.Add("authorized_key[]", k)
	}
	for _, a := range p.Addons {
		f.Add("addon[]", a)
	}
	if p.Test {
		f.Set("test", "true")
	}

	b, err := c.do("POST", "/order/server/transaction", f, 201, 200)
	if err != nil {
		return nil, err
	}
	var env transactionEnv
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, err
	}
	return &env.Transaction, nil
}

func (c *Client) GetOrderTransaction(id string) (*Transaction, error) {
	b, err := c.do("GET", "/order/server/transaction/"+url.PathEscape(id), nil, 200)
	if err != nil {
		return nil, err
	}
	var env transactionEnv
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, err
	}
	return &env.Transaction, nil
}

// --- Rescue + Reset

type RescueParams struct {
	OS            string
	AuthorizedFPs []string
}

func (c *Client) ActivateRescue(serverNumber int, p RescueParams) (*Rescue, error) {
	if p.OS == "" {
		p.OS = "linux"
	}
	f := url.Values{}
	f.Set("os", p.OS)
	for _, fp := range p.AuthorizedFPs {
		f.Add("authorized_key[]", fp)
	}

	b, err := c.do("POST", fmt.Sprintf("/boot/%d/rescue", serverNumber), f, 200)
	if err != nil {
		return nil, err
	}
	var env rescueEnv
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, err
	}
	return &env.Rescue, nil
}

func (c *Client) Reset(serverNumber int, typ string) error {
	if typ == "" {
		typ = "hw"
	}
	f := url.Values{}
	f.Set("type", typ)
	_, err := c.do("POST", fmt.Sprintf("/reset/%d", serverNumber), f, 200)
	return err
}

func (c *Client) CancelServer(serverNumber int, cancelDate string) error {
	f := url.Values{}
	if cancelDate != "" {
		f.Set("cancellation_date", cancelDate)
	}
	_, err := c.do("POST", fmt.Sprintf("/server/%d/cancel", serverNumber), f, 200)
	return err
}

func (c *Client) RevokeServerCancellation(serverNumber int) error {
	_, err := c.do("DELETE", fmt.Sprintf("/server/%d/cancel", serverNumber), nil, 200)
	return err
}

func (c *Client) SetServerName(serverNumber int, serverName string) error {
	f := url.Values{}
	f.Set("server_name", serverName)
	_, err := c.do("POST", fmt.Sprintf("/server/%d", serverNumber), f, 200)
	return err
}

// --- VSwitch

func (c *Client) CreateVSwitch(vlan int, name string) (*VSwitch, error) {
	f := url.Values{}
	f.Set("vlan", fmt.Sprintf("%d", vlan))
	f.Set("name", name)
	
	b, err := c.do("POST", "/vswitch", f, 201, 200)
	if err != nil {
		return nil, err
	}
	
	var env vswitchEnv
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, err
	}
	return &env.VSwitch, nil
}

func (c *Client) GetVSwitch(id int) (*VSwitch, error) {
	b, err := c.do("GET", fmt.Sprintf("/vswitch/%d", id), nil, 200)
	if err != nil {
		return nil, err
	}
	
	var env vswitchEnv
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, err
	}
	return &env.VSwitch, nil
}

func (c *Client) ListVSwitches() ([]VSwitch, error) {
	b, err := c.do("GET", "/vswitch", nil, 200)
	if err != nil {
		return nil, err
	}
	
	var env vswitchListEnv
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, err
	}
	return env.VSwitches, nil
}

func (c *Client) UpdateVSwitch(id int, vlan int, name string) (*VSwitch, error) {
	f := url.Values{}
	f.Set("vlan", fmt.Sprintf("%d", vlan))
	f.Set("name", name)
	
	b, err := c.do("POST", fmt.Sprintf("/vswitch/%d", id), f, 200)
	if err != nil {
		return nil, err
	}
	
	var env vswitchEnv
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, err
	}
	return &env.VSwitch, nil
}

func (c *Client) DeleteVSwitch(id int) error {
	_, err := c.do("DELETE", fmt.Sprintf("/vswitch/%d", id), nil, 200)
	return err
}

func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "404") || strings.Contains(s, "not found")
}
