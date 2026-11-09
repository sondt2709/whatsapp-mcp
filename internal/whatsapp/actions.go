package whatsapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/sondt2709/whatsapp-mcp/internal/api"
	"github.com/sondt2709/whatsapp-mcp/internal/store"
)

// Client is the part of the whatsmeow client that sending and downloading
// use. Tests replace it. It has no method that sends a read receipt or
// presence, so these actions cannot mark a message as read.
type Client interface {
	SendMessage(ctx context.Context, to types.JID, m *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error)
	Upload(ctx context.Context, plaintext []byte, appInfo whatsmeow.MediaType) (whatsmeow.UploadResponse, error)
	Download(ctx context.Context, msg whatsmeow.DownloadableMessage) ([]byte, error)
	IsOnWhatsApp(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error)
}

// Actions are the functions that use the stored data and the client.
type Actions struct {
	DB   *store.DB
	Cli  Client // nil for the functions that only read the database
	Rows Rows
	Now  func() time.Time
}

// Target is a resolved chat input.
type Target struct {
	ID    string    // canonical chat ID
	JID   types.JID // address to send to
	Known bool      // the chat index has it
	Phone string    // digits, when the input was a phone address
}

// ResolveChat changes a chat input (canonical ID, other address form or
// +<phone>) to the canonical chat ID.
func (a Actions) ResolveChat(ctx context.Context, input string) (Target, error) {
	j, err := ParseAddress(input)
	if err != nil {
		return Target{}, err
	}
	var t Target
	switch {
	case j.Server == types.GroupServer:
		t.JID = j
	case a.Rows.Me.Is(j):
		t.JID = a.Rows.Me.ID()
	default:
		t.JID = a.Rows.Res.User(ctx, j, types.JID{})
		if j.Server == types.DefaultUserServer {
			t.Phone = j.User
		}
		if t.JID.Server == types.DefaultUserServer {
			if c, ok, err := a.DB.ChatByPhone(ctx, t.JID.User); err == nil && ok {
				if cj, err := types.ParseJID(c.ID); err == nil {
					t.JID = cj
				}
			}
		}
	}
	t.ID = t.JID.String()
	_, t.Known, err = a.DB.GetChat(ctx, t.ID)
	return t, err
}

// recipient resolves a send target: a known chat, or a phone number that
// WhatsApp confirms.
func (a Actions) recipient(ctx context.Context, input string) (Target, error) {
	t, err := a.ResolveChat(ctx, input)
	if err != nil || t.Known {
		return t, err
	}
	if t.Phone == "" {
		return Target{}, api.Errorf(api.RecipientUnknown, "%q is not a known chat: use list_chats, or give +<phone> of a WhatsApp user", input)
	}
	res, err := a.Cli.IsOnWhatsApp(ctx, []string{"+" + t.Phone})
	if err != nil {
		return Target{}, classify(err)
	}
	if len(res) == 0 || !res[0].IsIn {
		return Target{}, api.Errorf(api.RecipientUnknown, "+%s is not a WhatsApp user", t.Phone)
	}
	t.JID = a.Rows.Res.User(ctx, res[0].JID, types.JID{})
	t.ID = t.JID.String()
	return t, nil
}

// sendID returns the WhatsApp ID of a send. One request ID always gives the
// same ID, so WhatsApp shows one message when a client sends again.
func (a Actions) sendID(chat, requestID string) types.MessageID {
	if requestID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(a.Rows.Me.ID().String() + "\x00" + chat + "\x00" + requestID))
	return types.MessageID(whatsmeow.WebMessageIDPrefix + strings.ToUpper(hex.EncodeToString(sum[:9])))
}

// expiration returns the disappearing-message timer of a stored row.
func expiration(r store.Row) uint32 {
	m := &waE2E.Message{}
	if proto.Unmarshal(r.Raw, m) != nil {
		return 0
	}
	return contextInfo(m).GetExpiration()
}

// timer returns the timer of the chat: from the quoted row, else from the
// newest stored row of the chat.
func (a Actions) timer(ctx context.Context, chat string, quoted *store.Row) uint32 {
	if quoted != nil {
		return expiration(*quoted)
	}
	if r, ok, err := a.DB.Newest(ctx, chat); err == nil && ok {
		return expiration(r)
	}
	return 0
}

// SendText sends a text, optionally as a quote of a stored message.
func (a Actions) SendText(ctx context.Context, in api.SendMessageInput) (api.SendResult, error) {
	if strings.TrimSpace(in.Text) == "" {
		return api.SendResult{}, api.Errorf(api.InvalidArgument, "the text is empty")
	}
	t, err := a.recipient(ctx, in.ChatID)
	if err != nil {
		return api.SendResult{}, err
	}
	var quoted *store.Row
	if in.QuoteMessageID != "" {
		chat, id, err := SplitMessageID(in.QuoteMessageID)
		if err != nil {
			return api.SendResult{}, err
		}
		qt, err := a.ResolveChat(ctx, chat)
		if err != nil {
			return api.SendResult{}, err
		}
		if qt.ID != t.ID {
			return api.SendResult{}, api.Errorf(api.InvalidArgument, "the quoted message is in another chat")
		}
		r, ok, err := a.DB.Message(ctx, qt.ID, id)
		if err != nil {
			return api.SendResult{}, err
		}
		if !ok {
			return api.SendResult{}, api.Errorf(api.NotFound, "the message %s is not stored (older than the retention time?): send without a quote", in.QuoteMessageID)
		}
		quoted = &r
	}
	ci := &waE2E.ContextInfo{}
	if e := a.timer(ctx, t.ID, quoted); e > 0 {
		ci.Expiration = proto.Uint32(e)
	}
	if quoted != nil {
		target := &waE2E.Message{}
		if err := proto.Unmarshal(quoted.Raw, target); err != nil {
			return api.SendResult{}, fmt.Errorf("read the stored message: %w", err)
		}
		ci.StanzaID = proto.String(quoted.ID)
		ci.Participant = proto.String(quoted.Sender)
		ci.QuotedMessage = target
	}
	msg := &waE2E.Message{Conversation: proto.String(in.Text)}
	if ci.Expiration != nil || ci.StanzaID != nil {
		msg = &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(in.Text), ContextInfo: ci}}
	}
	return a.send(ctx, t, msg, in.RequestID)
}

// SendFile sends a file: an image as an image, each other file as a document.
func (a Actions) SendFile(ctx context.Context, chat, name, caption, requestID string, data []byte) (api.SendResult, error) {
	if len(data) == 0 {
		return api.SendResult{}, api.Errorf(api.InvalidArgument, "the file is empty")
	}
	if len(data) > api.MaxFileSize {
		return api.SendResult{}, api.Errorf(api.TooLarge, "the file has %d bytes; the maximum is %d", len(data), api.MaxFileSize)
	}
	t, err := a.recipient(ctx, chat)
	if err != nil {
		return api.SendResult{}, err
	}
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	mimeType, _, _ = strings.Cut(mimeType, ";")
	image := mimeType == "image/jpeg" || mimeType == "image/png" || mimeType == "image/webp"
	kind := whatsmeow.MediaDocument
	if image {
		kind = whatsmeow.MediaImage
	}
	up, err := a.Cli.Upload(ctx, data, kind)
	if err != nil {
		return api.SendResult{}, classify(err)
	}
	var ci *waE2E.ContextInfo
	if e := a.timer(ctx, t.ID, nil); e > 0 {
		ci = &waE2E.ContextInfo{Expiration: proto.Uint32(e)}
	}
	var msg *waE2E.Message
	if image {
		msg = &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			Mimetype: proto.String(mimeType), Caption: optional(caption), ContextInfo: ci,
		}}
	} else {
		msg = &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256, FileLength: proto.Uint64(up.FileLength),
			Mimetype: proto.String(mimeType), FileName: proto.String(name), Title: proto.String(name),
			Caption: optional(caption), ContextInfo: ci,
		}}
	}
	return a.send(ctx, t, msg, requestID)
}

func optional(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return proto.String(s)
}

// send sends the message and stores it, so that list_messages shows it.
// whatsmeow does not give the own sends of this device back as events.
func (a Actions) send(ctx context.Context, t Target, msg *waE2E.Message, requestID string) (api.SendResult, error) {
	resp, err := a.Cli.SendMessage(ctx, t.JID, msg, whatsmeow.SendRequestExtra{ID: a.sendID(t.ID, requestID)})
	if err != nil {
		return api.SendResult{}, classify(err)
	}
	raw, _ := proto.Marshal(msg)
	when := resp.Timestamp
	if when.IsZero() {
		when = a.now()
	}
	row := store.Row{Chat: t.ID, ID: string(resp.ID), Sender: a.Rows.Me.ID().String(), FromMe: true, Time: when, Text: Body(msg), Raw: raw}
	if m, ok := MediaOf(msg); ok {
		row.MediaKind, row.MediaName, row.MediaMIME, row.MediaSize = m.Kind, m.Name, m.MIME, int64(m.Length)
	}
	if id, _, ok := quoteRef(msg); ok {
		row.QuoteID, row.QuoteAuth, row.QuoteText = id, "", QuotedText(msg)
	}
	if _, err := a.DB.SaveMessage(ctx, row); err != nil {
		return api.SendResult{}, fmt.Errorf("the message was sent, but storing it failed: %w", err)
	}
	kind, _ := ChatKind(t.JID)
	_ = a.DB.SaveChat(ctx, store.Chat{ID: t.ID, Kind: kind, Phone: t.Phone, Last: when})
	return api.SendResult{MessageID: MessageID(t.ID, string(resp.ID))}, nil
}

func (a Actions) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Download returns the bytes and the description of the file of a stored message.
func (a Actions) Download(ctx context.Context, fileID string) ([]byte, api.File, error) {
	chat, id, err := SplitMessageID(fileID)
	if err != nil {
		return nil, api.File{}, err
	}
	t, err := a.ResolveChat(ctx, chat)
	if err != nil {
		return nil, api.File{}, err
	}
	row, ok, err := a.DB.Message(ctx, t.ID, id)
	if err != nil {
		return nil, api.File{}, err
	}
	if !ok {
		return nil, api.File{}, api.Errorf(api.NotFound, "the message %s is not stored (older than the retention time?)", fileID)
	}
	msg := &waE2E.Message{}
	if err := proto.Unmarshal(row.Raw, msg); err != nil {
		return nil, api.File{}, fmt.Errorf("read the stored message: %w", err)
	}
	media, ok := MediaOf(msg)
	if !ok {
		return nil, api.File{}, api.Errorf(api.NoFile, "the message %s has no file", fileID)
	}
	f := api.File{FileID: MessageID(t.ID, id), Kind: media.Kind, Name: media.Name, MIME: media.MIME, Size: int64(media.Length)}
	if media.Length > api.MaxFileSize {
		return nil, f, api.Errorf(api.TooLarge, "the file has %d bytes; the maximum is %d", media.Length, api.MaxFileSize)
	}
	data, err := a.Cli.Download(ctx, media.Part)
	if err != nil {
		var he whatsmeow.DownloadHTTPError
		if errors.As(err, &he) && (he.StatusCode == 403 || he.StatusCode == 404 || he.StatusCode == 410) {
			return nil, f, api.Errorf(api.FileExpired, "WhatsApp no longer keeps this file (HTTP %d); it is only on the phone now", he.StatusCode)
		}
		return nil, f, classify(err)
	}
	f.Size = int64(len(data))
	return data, f, nil
}

// classify gives a whatsmeow error a code.
func classify(err error) error {
	var e *api.Error
	switch {
	case errors.As(err, &e):
		return err
	case errors.Is(err, whatsmeow.ErrNotConnected), errors.Is(err, whatsmeow.ErrNotLoggedIn):
		return api.Errorf(api.NotConnected, "no connection to WhatsApp: %v", err)
	case errors.Is(err, whatsmeow.ErrNoPermissionInGroup):
		return api.Errorf(api.InvalidArgument, "the account cannot send in this group: %v", err)
	case errors.Is(err, whatsmeow.ErrRecipientADJID), errors.Is(err, whatsmeow.ErrUnknownServer):
		return api.Errorf(api.RecipientUnknown, "%v", err)
	}
	return err
}
