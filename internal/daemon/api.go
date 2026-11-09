package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/sondt2709/whatsapp-mcp/internal/api"
	"github.com/sondt2709/whatsapp-mcp/internal/store"
)

// FileHeader carries the description of a downloaded file (base64 of JSON
// api.File), because the body holds the bytes.
const FileHeader = "X-Whatsapp-Mcp-File"

// ListChats returns the chats of the index.
func (d *Daemon) ListChats(ctx context.Context, in api.ListChatsInput) (api.ListChatsResult, error) {
	db, _, err := d.session()
	if err != nil {
		return api.ListChatsResult{}, err
	}
	if in.Kind != "" && in.Kind != api.KindDirect && in.Kind != api.KindGroup {
		return api.ListChatsResult{}, api.Errorf(api.InvalidArgument, "kind %q is not direct or group", in.Kind)
	}
	rows, err := db.Chats(ctx, in.Query, in.Kind, api.Limit(in.Limit))
	if err != nil {
		return api.ListChatsResult{}, err
	}
	out := api.ListChatsResult{Chats: make([]api.Chat, 0, len(rows))}
	for _, c := range rows {
		out.Chats = append(out.Chats, chat(c))
	}
	return out, nil
}

// ListMessages returns stored messages, the newest first.
func (d *Daemon) ListMessages(ctx context.Context, in api.ListMessagesInput) (api.ListMessagesResult, error) {
	a, _, err := d.actions()
	if err != nil {
		return api.ListMessagesResult{}, err
	}
	f := store.Filter{Query: in.Query, Limit: api.Limit(in.Limit)}
	if f.Before, err = parseTime(in.Before, "before"); err != nil {
		return api.ListMessagesResult{}, err
	}
	if f.After, err = parseTime(in.After, "after"); err != nil {
		return api.ListMessagesResult{}, err
	}
	if in.ChatID != "" {
		t, err := a.ResolveChat(ctx, in.ChatID)
		if err != nil {
			return api.ListMessagesResult{}, err
		}
		if !t.Known {
			return api.ListMessagesResult{}, api.Errorf(api.NotFound, "the chat %q is not known: use list_chats", in.ChatID)
		}
		f.Chat = t.ID
	}
	last, err := a.DB.LastRow(ctx)
	if err != nil {
		return api.ListMessagesResult{}, err
	}
	rows, err := a.DB.Messages(ctx, f)
	if err != nil {
		return api.ListMessagesResult{}, err
	}
	return api.ListMessagesResult{Messages: messages(rows), Cursor: Cursor(last)}, nil
}

// Wait returns the messages after the cursor, the oldest first. It waits up
// to the timeout when there are none.
func (d *Daemon) Wait(ctx context.Context, in api.WaitInput) (api.WaitResult, error) {
	a, _, err := d.actions()
	if err != nil {
		return api.WaitResult{}, err
	}
	var row int64
	if in.Cursor != "" {
		if row, err = ParseCursor(in.Cursor); err != nil {
			return api.WaitResult{}, err
		}
	} else if row, err = a.DB.LastRow(ctx); err != nil {
		return api.WaitResult{}, err
	}
	chatID := ""
	if in.ChatID != "" {
		t, err := a.ResolveChat(ctx, in.ChatID)
		if err != nil {
			return api.WaitResult{}, err
		}
		chatID = t.ID
	}
	timer := time.NewTimer(time.Duration(api.Wait(in.TimeoutSeconds)) * time.Second)
	defer timer.Stop()
	for {
		woken := d.hub.Wait() // take the channel before the read
		rows, err := a.DB.After(ctx, row, chatID, api.MaxLimit)
		if err != nil {
			return api.WaitResult{}, err
		}
		if len(rows) > 0 {
			return api.WaitResult{Messages: messages(rows), Cursor: Cursor(rows[len(rows)-1].Row)}, nil
		}
		select {
		case <-woken:
		case <-timer.C:
			return api.WaitResult{Messages: []api.Message{}, Cursor: Cursor(row), TimedOut: true}, nil
		case <-ctx.Done():
			return api.WaitResult{}, ctx.Err()
		}
	}
}

func (d *Daemon) canSend() error {
	if d.cfg.ReadOnly {
		return api.Errorf(api.ReadOnly, "the daemon runs with --read-only: sending is off")
	}
	return nil
}

// Send sends a text.
func (d *Daemon) Send(ctx context.Context, in api.SendMessageInput) (api.SendResult, error) {
	if err := d.canSend(); err != nil {
		return api.SendResult{}, err
	}
	a, be, err := d.actions()
	if err != nil {
		return api.SendResult{}, err
	}
	if err := be.Ready(); err != nil {
		return api.SendResult{}, err
	}
	res, err := a.SendText(ctx, in)
	if err == nil {
		d.hub.Notify()
	}
	return res, err
}

// SendFile sends a file that the client read.
func (d *Daemon) SendFile(ctx context.Context, chatID, name, caption, requestID string, data []byte) (api.SendResult, error) {
	if err := d.canSend(); err != nil {
		return api.SendResult{}, err
	}
	a, be, err := d.actions()
	if err != nil {
		return api.SendResult{}, err
	}
	if err := be.Ready(); err != nil {
		return api.SendResult{}, err
	}
	res, err := a.SendFile(ctx, chatID, name, caption, requestID, data)
	if err == nil {
		d.hub.Notify()
	}
	return res, err
}

// Download returns the bytes of the file of a stored message.
func (d *Daemon) Download(ctx context.Context, fileID string) ([]byte, api.File, error) {
	a, be, err := d.actions()
	if err != nil {
		return nil, api.File{}, err
	}
	if err := be.Ready(); err != nil {
		return nil, api.File{}, err
	}
	return a.Download(ctx, fileID)
}

// Handler returns the HTTP handler of the socket API.
func (d *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, d.Status(r.Context()))
	})
	mux.HandleFunc("POST /v1/chats", handle(d.ListChats))
	mux.HandleFunc("POST /v1/messages", handle(d.ListMessages))
	mux.HandleFunc("POST /v1/wait", handle(d.Wait))
	mux.HandleFunc("POST /v1/send", handle(d.Send))
	mux.HandleFunc("POST /v1/send-file", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(io.LimitReader(r.Body, api.MaxFileSize+1))
		if err != nil {
			writeError(w, err)
			return
		}
		if len(data) > api.MaxFileSize {
			writeError(w, api.Errorf(api.TooLarge, "the file is larger than %d bytes", api.MaxFileSize))
			return
		}
		q := r.URL.Query()
		res, err := d.SendFile(r.Context(), q.Get("chat_id"), q.Get("name"), q.Get("caption"), q.Get("request_id"), data)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, res)
	})
	mux.HandleFunc("POST /v1/download", func(w http.ResponseWriter, r *http.Request) {
		var in api.DownloadInput
		if err := decode(r, &in); err != nil {
			writeError(w, err)
			return
		}
		data, f, err := d.Download(r.Context(), in.FileID)
		if err != nil {
			writeError(w, err)
			return
		}
		meta, _ := json.Marshal(f)
		w.Header().Set(FileHeader, base64.StdEncoding.EncodeToString(meta))
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.Copy(w, bytes.NewReader(data))
	})
	mux.HandleFunc("POST /v1/pair", func(w http.ResponseWriter, r *http.Request) {
		var in PairRequest
		if err := decode(r, &in); err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		flush := func() {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		err := d.Pair(r.Context(), in, func(e PairEvent) {
			_ = enc.Encode(e)
			flush()
		})
		if err != nil {
			_ = enc.Encode(PairEvent{Error: asAPIError(err)})
		} else {
			_ = enc.Encode(PairEvent{Paired: true})
		}
		flush()
	})
	mux.HandleFunc("POST /v1/unlock", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Passphrase string `json:"passphrase"`
		}
		if err := decode(r, &in); err != nil {
			writeError(w, err)
			return
		}
		if err := d.Unlock(r.Context(), in.Passphrase); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, d.Status(r.Context()))
	})
	mux.HandleFunc("POST /v1/logout", func(w http.ResponseWriter, r *http.Request) {
		res, err := d.Logout(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, res)
	})
	return mux
}

// handle decodes the JSON input, calls fn and writes the JSON result.
func handle[In, Out any](fn func(context.Context, In) (Out, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in In
		if err := decode(r, &in); err != nil {
			writeError(w, err)
			return
		}
		out, err := fn(r.Context(), in)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, out)
	}
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return api.Errorf(api.InvalidArgument, "the request is not valid JSON: %v", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// asAPIError gives each error a code.
func asAPIError(err error) *api.Error {
	var e *api.Error
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return api.Errorf(api.Internal, "the request stopped: %v", err)
	}
	return api.Errorf(api.Internal, "%v", err)
}

// Status returns the HTTP status of an error code.
func httpStatus(c api.Code) int {
	switch c {
	case api.InvalidArgument:
		return http.StatusBadRequest
	case api.NotFound, api.NoFile:
		return http.StatusNotFound
	case api.PathNotAllowed, api.ReadOnly:
		return http.StatusForbidden
	case api.TooLarge:
		return http.StatusRequestEntityTooLarge
	case api.RecipientUnknown:
		return http.StatusUnprocessableEntity
	case api.FileExpired:
		return http.StatusGone
	case api.NotPaired, api.Locked, api.LoggedOut, api.NotConnected:
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

func writeError(w http.ResponseWriter, err error) {
	e := asAPIError(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus(e.Code))
	_ = json.NewEncoder(w).Encode(e)
}
