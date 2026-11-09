// Package client calls the daemon of a data folder through its Unix socket.
// It starts the daemon when none answers.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/sondt2709/whatsapp-mcp/internal/api"
	"github.com/sondt2709/whatsapp-mcp/internal/daemon"
)

// startWait limits the wait for the socket of a daemon that the client started.
const startWait = 10 * time.Second

// Client calls one daemon.
type Client struct {
	DataDir    string
	DaemonArgs []string // extra flags for a daemon that the client starts
	Exe        string   // the binary to start; default os.Executable
	NoStart    bool     // never start a daemon
	http       *http.Client
}

// New returns a client of the daemon of the data folder.
func New(dataDir string) *Client {
	c := &Client{DataDir: dataDir}
	c.http = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return daemon.Dial(ctx, c.DataDir)
		},
		DisableCompression: true,
	}}
	return c
}

// Ensure checks that a daemon answers, and starts one when none does.
func (c *Client) Ensure(ctx context.Context) error {
	if _, err := c.Status(ctx); err == nil || !isDial(err) {
		return err
	}
	if c.NoStart {
		return api.Errorf(api.NotConnected, "no whatsapp-mcp daemon runs for this data folder: run \"whatsapp-mcp daemon\"")
	}
	return c.start(ctx)
}

func isDial(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

// start runs "whatsapp-mcp daemon" in a new session, so that it keeps
// running after the client exits, and waits for its socket.
func (c *Client) start(ctx context.Context) error {
	exe := c.Exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(daemon.LogPath(c.DataDir), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, append([]string{"daemon", "--data-dir", c.DataDir}, c.DaemonArgs...)...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the daemon: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(startWait)
	for time.Now().Before(deadline) {
		if _, err := c.Status(ctx); err == nil {
			return nil
		}
		select {
		case err := <-exited:
			// Another client started a daemon at the same time: use it.
			if _, serr := c.Status(ctx); serr == nil {
				return nil
			}
			return fmt.Errorf("the daemon stopped (%v); see %s", err, daemon.LogPath(c.DataDir))
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("the daemon did not answer in %s; see %s", startWait, daemon.LogPath(c.DataDir))
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://whatsapp-mcp"+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		var e api.Error
		if json.NewDecoder(resp.Body).Decode(&e) == nil && e.Code != "" {
			return nil, &e
		}
		return nil, fmt.Errorf("daemon answered HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

func (c *Client) call(ctx context.Context, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, path, bytes.NewReader(b), "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// Status returns the account state.
func (c *Client) Status(ctx context.Context) (api.Status, error) {
	var s api.Status
	resp, err := c.do(ctx, http.MethodGet, "/v1/status", nil, "")
	if err != nil {
		return s, err
	}
	defer resp.Body.Close()
	return s, json.NewDecoder(resp.Body).Decode(&s)
}

// ListChats calls list_chats.
func (c *Client) ListChats(ctx context.Context, in api.ListChatsInput) (api.ListChatsResult, error) {
	var out api.ListChatsResult
	return out, c.call(ctx, "/v1/chats", in, &out)
}

// ListMessages calls list_messages.
func (c *Client) ListMessages(ctx context.Context, in api.ListMessagesInput) (api.ListMessagesResult, error) {
	var out api.ListMessagesResult
	return out, c.call(ctx, "/v1/messages", in, &out)
}

// Wait calls wait_for_messages.
func (c *Client) Wait(ctx context.Context, in api.WaitInput) (api.WaitResult, error) {
	var out api.WaitResult
	return out, c.call(ctx, "/v1/wait", in, &out)
}

// Send calls send_message.
func (c *Client) Send(ctx context.Context, in api.SendMessageInput) (api.SendResult, error) {
	var out api.SendResult
	return out, c.call(ctx, "/v1/send", in, &out)
}

// SendFile streams a file to the daemon for send_file.
func (c *Client) SendFile(ctx context.Context, chatID, name, caption, requestID string, r io.Reader) (api.SendResult, error) {
	q := url.Values{"chat_id": {chatID}, "name": {name}, "caption": {caption}, "request_id": {requestID}}
	resp, err := c.do(ctx, http.MethodPost, "/v1/send-file?"+q.Encode(), r, "application/octet-stream")
	if err != nil {
		return api.SendResult{}, err
	}
	defer resp.Body.Close()
	var out api.SendResult
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

// Download returns the bytes of a file as a stream and its description. The
// caller closes the stream.
func (c *Client) Download(ctx context.Context, fileID string) (io.ReadCloser, api.File, error) {
	b, _ := json.Marshal(api.DownloadInput{FileID: fileID})
	resp, err := c.do(ctx, http.MethodPost, "/v1/download", bytes.NewReader(b), "application/json")
	if err != nil {
		return nil, api.File{}, err
	}
	var f api.File
	meta, err := base64.StdEncoding.DecodeString(resp.Header.Get(daemon.FileHeader))
	if err != nil || json.Unmarshal(meta, &f) != nil {
		resp.Body.Close()
		return nil, api.File{}, errors.New("the daemon gave no file description")
	}
	return resp.Body, f, nil
}

// Pair runs the pairing in the daemon and calls show for each step.
func (c *Client) Pair(ctx context.Context, in daemon.PairRequest, show func(daemon.PairEvent)) error {
	b, _ := json.Marshal(in)
	resp, err := c.do(ctx, http.MethodPost, "/v1/pair", bytes.NewReader(b), "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e daemon.PairEvent
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return err
		}
		switch {
		case e.Error != nil:
			return e.Error
		case e.Paired:
			return nil
		default:
			show(e)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("the pairing stopped with no result")
}

// Unlock sends the passphrase of the data folder.
func (c *Client) Unlock(ctx context.Context, passphrase string) (api.Status, error) {
	var out api.Status
	return out, c.call(ctx, "/v1/unlock", map[string]string{"passphrase": passphrase}, &out)
}

// Logout unlinks the device and deletes the local data.
func (c *Client) Logout(ctx context.Context) (daemon.LogoutResult, error) {
	var out daemon.LogoutResult
	return out, c.call(ctx, "/v1/logout", struct{}{}, &out)
}

// IsNoDaemon reports whether err means that no daemon answered.
func IsNoDaemon(err error) bool {
	return err != nil && (isDial(err) || strings.Contains(err.Error(), "no whatsapp-mcp daemon"))
}
