package daemon

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/sondt2709/whatsapp-mcp/internal/api"
	"github.com/sondt2709/whatsapp-mcp/internal/sanitize"
	"github.com/sondt2709/whatsapp-mcp/internal/store"
	"github.com/sondt2709/whatsapp-mcp/internal/whatsapp"
)

// Cursor encodes a row number as an opaque cursor.
func Cursor(row int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte("r" + strconv.FormatInt(row, 10)))
}

// ParseCursor decodes a cursor.
func ParseCursor(s string) (int64, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || !strings.HasPrefix(string(b), "r") {
		return 0, api.Errorf(api.InvalidArgument, "%q is not a cursor of whatsapp-mcp", s)
	}
	n, err := strconv.ParseInt(string(b[1:]), 10, 64)
	if err != nil || n < 0 {
		return 0, api.Errorf(api.InvalidArgument, "%q is not a cursor of whatsapp-mcp", s)
	}
	return n, nil
}

// message converts a stored row. Text, names and quotes lose their hidden
// characters, because they come from other people.
func message(r store.Row) api.Message {
	m := api.Message{
		MessageID:  whatsapp.MessageID(r.Chat, r.ID),
		ChatID:     r.Chat,
		ChatName:   sanitize.Line(r.ChatName),
		SenderID:   r.Sender,
		SenderName: sanitize.Line(r.SenderName),
		FromMe:     r.FromMe,
		Time:       r.Time.UTC(),
		Text:       sanitize.Text(r.Text),
	}
	if r.MediaKind != "" {
		m.File = &api.File{FileID: m.MessageID, Kind: r.MediaKind, Name: sanitize.Line(r.MediaName), MIME: sanitize.Line(r.MediaMIME), Size: r.MediaSize}
	}
	if r.QuoteID != "" {
		m.Quote = &api.Quote{MessageID: whatsapp.MessageID(r.Chat, r.QuoteID), Author: sanitize.Line(r.QuoteAuth), Text: sanitize.Line(r.QuoteText)}
	}
	return m
}

func messages(rows []store.Row) []api.Message {
	out := make([]api.Message, 0, len(rows))
	for _, r := range rows {
		out = append(out, message(r))
	}
	return out
}

func chat(c store.Chat) api.Chat {
	out := api.Chat{ChatID: c.ID, Name: sanitize.Line(c.Name), Kind: c.Kind}
	if c.Phone != "" {
		out.Phone = "+" + c.Phone
	}
	if !c.Last.IsZero() {
		t := c.Last.UTC()
		out.LastMessageAt = &t
	}
	return out
}

func parseTime(s, field string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, api.Errorf(api.InvalidArgument, "%s %q is not an RFC 3339 time (for example 2026-01-31T09:00:00Z)", field, s)
	}
	return t, nil
}
