package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/probe"
)

// Client communicates with a remote or local strife-agent daemon.
type Client struct {
	addr       string
	baseURL    string
	httpClient *http.Client
	token      string
}

// NewClient connects to strife-agent at addr (e.g. "localhost:9292", "10.0.0.1:9292", or "/var/run/strife-agent.sock").
func NewClient(addr, token string) *Client {
	if addr == "" {
		addr = fmt.Sprintf("127.0.0.1:%d", DefaultPort)
	}

	var tr *http.Transport
	baseURL := ""

	if strings.HasPrefix(addr, "/") || strings.HasSuffix(addr, ".sock") {
		// Unix domain socket
		baseURL = "http://unix"
		tr = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", addr)
			},
			DisableKeepAlives: false,
		}
	} else {
		// TCP
		if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
			baseURL = "http://" + addr
		} else {
			baseURL = addr
		}
		tr = &http.Transport{
			DisableKeepAlives:   false,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		}
	}

	return &Client{
		addr:    addr,
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Transport: tr,
			Timeout:   30 * time.Second,
		},
		token: token,
	}
}

// Ping checks the health of the agent and returns its capabilities.
func (c *Client) Ping(ctx context.Context) (*StatusResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent ping %s: %w", c.addr, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("agent ping %s returned status %d: %s", c.addr, resp.StatusCode, string(body))
	}

	var status StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("agent ping %s decode: %w", c.addr, err)
	}
	return &status, nil
}

// Apply sends an ApplyRequest to the agent and returns its ApplyResponse with microsecond timings.
func (c *Client) Apply(ctx context.Context, applyReq ApplyRequest) (*ApplyResponse, error) {
	data, err := json.Marshal(applyReq)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/apply", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent apply %s: %w", c.addr, err)
	}
	defer resp.Body.Close()

	var applyResp ApplyResponse
	if err := json.NewDecoder(resp.Body).Decode(&applyResp); err != nil {
		return nil, fmt.Errorf("agent apply %s decode: %w", c.addr, err)
	}
	if !applyResp.Success && applyResp.Error != "" {
		return &applyResp, fmt.Errorf("agent apply error: %s", applyResp.Error)
	}
	return &applyResp, nil
}

// Exec executes an arbitrary shell script on the node via strife-agent.
func (c *Client) Exec(ctx context.Context, script string) (string, error) {
	execReq := ExecRequest{Script: script}
	data, err := json.Marshal(execReq)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/exec", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("agent exec %s: %w", c.addr, err)
	}
	defer resp.Body.Close()

	var execResp ExecResponse
	if err := json.NewDecoder(resp.Body).Decode(&execResp); err != nil {
		return "", fmt.Errorf("agent exec %s decode: %w", c.addr, err)
	}
	if execResp.ExitCode != 0 || execResp.Error != "" {
		return execResp.Output, fmt.Errorf("agent exec %s exited %d: %s", c.addr, execResp.ExitCode, execResp.Error)
	}
	return execResp.Output, nil
}

// Teardown resets all faults on the agent node.
func (c *Client) Teardown(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/teardown", nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("agent teardown %s: %w", c.addr, err)
	}
	defer resp.Body.Close()
	return nil
}

// Probes fetches server-side diagnostic telemetry events from the agent node.
func (c *Client) Probes(ctx context.Context, reset bool) ([]probe.Event, error) {
	url := c.baseURL + "/probes"
	if reset {
		url += "?reset=1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent probes %s: %w", c.addr, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("agent probes %s returned status %d: %s", c.addr, resp.StatusCode, string(body))
	}

	var probesResp ProbesResponse
	if err := json.NewDecoder(resp.Body).Decode(&probesResp); err != nil {
		return nil, fmt.Errorf("agent probes %s decode: %w", c.addr, err)
	}
	return probesResp.Events, nil
}
