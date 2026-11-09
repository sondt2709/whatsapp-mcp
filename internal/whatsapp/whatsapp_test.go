package whatsapp

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sondt2709/whatsapp-mcp/internal/api"
	"github.com/sondt2709/whatsapp-mcp/internal/store"
)

// lids is a fake LID store.
type lids map[string]string // phone user → lid user

func (l lids) GetLIDForPN(_ context.Context, pn types.JID) (types.JID, error) {
	if u, ok := l[pn.User]; ok {
		return types.NewJID(u, types.HiddenUserServer), nil
	}
	return types.JID{}, nil
}

func (l lids) GetPNForLID(_ context.Context, lid types.JID) (types.JID, error) {
	for p, u := range l {
		if u == lid.User {
			return types.NewJID(p, types.DefaultUserServer), nil
		}
	}
	return types.JID{}, nil
}

var (
	pn    = func(u string) types.JID { return types.NewJID(u, types.DefaultUserServer) }
	lid   = func(u string) types.JID { return types.NewJID(u, types.HiddenUserServer) }
	me    = Me{PN: pn("15550000001"), LID: lid("900001")}
	known = lids{"15550000001": "900001", "15550000002": "900002"}
)

func rows() Rows { return Rows{Res: Resolver{LIDs: known}, Me: me} }

func TestResolverUser(t *testing.T) {
	ctx := context.Background()
	r := Resolver{LIDs: known}
	cases := []struct {
		in, alt types.JID
		want    string
	}{
		{pn("15550000002"), types.JID{}, "900002@lid"},                 // mapping in the LID store
		{pn("15550000003"), lid("900003"), "900003@lid"},               // mapping from the event
		{pn("15550000004"), types.JID{}, "15550000004@s.whatsapp.net"}, // unknown: keep the phone form
		{lid("900002"), types.JID{}, "900002@lid"},
		{types.NewADJID("15550000002", 0, 7), types.JID{}, "900002@lid"}, // device part removed
	}
	for _, c := range cases {
		if got := r.User(ctx, c.in, c.alt).String(); got != c.want {
			t.Errorf("User(%s, %s) = %s, want %s", c.in, c.alt, got, c.want)
		}
	}
	if p := r.Phone(ctx, lid("900002"), types.JID{}); p != "15550000002" {
		t.Errorf("Phone of a known LID = %q", p)
	}
}

func TestParseAddress(t *testing.T) {
	for in, want := range map[string]string{
		"+1 555 000 0002":              "15550000002@s.whatsapp.net",
		"15550000002":                  "15550000002@s.whatsapp.net",
		"900002@lid":                   "900002@lid",
		"123-456@g.us":                 "123-456@g.us",
		"15550000002:7@s.whatsapp.net": "15550000002@s.whatsapp.net",
	} {
		j, err := ParseAddress(in)
		if err != nil || j.String() != want {
			t.Errorf("ParseAddress(%q) = %s, %v; want %s", in, j, err, want)
		}
	}
	for _, in := range []string{"", "+abc", "status@broadcast", "1@newsletter", "x@unknown.server"} {
		if _, err := ParseAddress(in); api.CodeOf(err) != api.InvalidArgument {
			t.Errorf("ParseAddress(%q): err = %v, want invalid_argument", in, err)
		}
	}
	chat, id, err := SplitMessageID("123-456@g.us/3EB0ABC")
	if err != nil || chat != "123-456@g.us" || id != "3EB0ABC" {
		t.Errorf("SplitMessageID = %q %q %v", chat, id, err)
	}
	if _, _, err := SplitMessageID("3EB0ABC"); err == nil {
		t.Error("SplitMessageID accepted an ID without a chat")
	}
}

func event(chat, sender, senderAlt types.JID, fromMe bool, m *waE2E.Message) *events.Message {
	return &events.Message{Info: types.MessageInfo{
		MessageSource: types.MessageSource{Chat: chat, Sender: sender, SenderAlt: senderAlt, IsFromMe: fromMe},
		ID:            "3EB0" + strings.ToUpper(chat.User), Timestamp: time.Unix(1_700_000_000, 0), PushName: "Push Name",
	}, Message: m}
}

func TestFromEvent(t *testing.T) {
	ctx := context.Background()
	r := rows()

	// A direct message with the phone address: the chat and the sender use the LID.
	row, ok := r.FromEvent(ctx, event(pn("15550000002"), pn("15550000002"), types.JID{}, false,
		&waE2E.Message{Conversation: proto.String("hello")}))
	if !ok || row.Chat != "900002@lid" || row.Sender != "900002@lid" || row.Text != "hello" || row.SenderName != "Push Name" {
		t.Fatalf("direct message: %+v %v", row, ok)
	}

	// The own chat with the phone address becomes the own LID, also when
	// the LID store does not map the own number.
	row, _ = Rows{Res: Resolver{LIDs: lids{}}, Me: me}.FromEvent(ctx, event(pn("15550000001"), pn("15550000001"), types.JID{}, true,
		&waE2E.Message{Conversation: proto.String("note")}))
	if row.Chat != "900001@lid" || row.Sender != "900001@lid" || !row.FromMe {
		t.Fatalf("own chat: %+v", row)
	}

	// A group document with a quote of a message of the user.
	doc := &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("report.pdf"), Mimetype: proto.String("application/pdf"),
		FileLength: proto.Uint64(1234), ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("Q1"), Participant: proto.String("900001@lid"),
			QuotedMessage: &waE2E.Message{Conversation: proto.String("the earlier text")}}}}
	row, ok = r.FromEvent(ctx, event(types.NewJID("123", types.GroupServer), lid("900002"), types.JID{}, false, doc))
	if !ok || row.Chat != "123@g.us" || row.MediaKind != "document" || row.MediaName != "report.pdf" || row.MediaSize != 1234 ||
		row.QuoteID != "Q1" || row.QuoteAuth != "me" || row.QuoteText != "the earlier text" || row.Text != "[document: report.pdf]" {
		t.Fatalf("group document: %+v", row)
	}

	// Messages that are not kept.
	for name, e := range map[string]*events.Message{
		"reaction":  event(pn("15550000002"), pn("15550000002"), types.JID{}, false, &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Text: proto.String("+")}}),
		"broadcast": event(types.NewJID("status", types.BroadcastServer), pn("15550000002"), types.JID{}, false, &waE2E.Message{Conversation: proto.String("x")}),
		"channel":   event(types.NewJID("1", types.NewsletterServer), pn("15550000002"), types.JID{}, false, &waE2E.Message{Conversation: proto.String("x")}),
	} {
		if _, ok := r.FromEvent(ctx, e); ok {
			t.Errorf("%s was kept", name)
		}
	}
	edit := event(pn("15550000002"), pn("15550000002"), types.JID{}, false, &waE2E.Message{Conversation: proto.String("x")})
	edit.IsEdit = true
	if _, ok := r.FromEvent(ctx, edit); ok {
		t.Error("an edit was kept")
	}
}

// fake is a whatsmeow client that records the sends.
type fake struct {
	sent      []*waE2E.Message
	to        []types.JID
	ids       []types.MessageID
	uploads   []whatsmeow.MediaType
	download  []byte
	dlErr     error
	onWA      map[string]types.JID
	sendError error
}

func (f *fake) SendMessage(_ context.Context, to types.JID, m *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	if f.sendError != nil {
		return whatsmeow.SendResponse{}, f.sendError
	}
	id := extra[0].ID
	if id == "" {
		id = types.MessageID("RANDOM" + string(rune('A'+len(f.sent))))
	}
	f.sent, f.to, f.ids = append(f.sent, m), append(f.to, to), append(f.ids, id)
	return whatsmeow.SendResponse{ID: id, Timestamp: time.Unix(1_700_000_100, 0)}, nil
}

func (f *fake) Upload(_ context.Context, data []byte, t whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	f.uploads = append(f.uploads, t)
	return whatsmeow.UploadResponse{URL: "u", DirectPath: "/d", MediaKey: []byte{1}, FileLength: uint64(len(data))}, nil
}

func (f *fake) Download(context.Context, whatsmeow.DownloadableMessage) ([]byte, error) {
	return f.download, f.dlErr
}

func (f *fake) IsOnWhatsApp(_ context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
	var out []types.IsOnWhatsAppResponse
	for _, p := range phones {
		j, ok := f.onWA[strings.TrimPrefix(p, "+")]
		out = append(out, types.IsOnWhatsAppResponse{Query: p, JID: j, IsIn: ok})
	}
	return out, nil
}

func setup(t *testing.T) (Actions, *fake) {
	t.Helper()
	ctx := context.Background()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	db, err := store.Open(ctx, t.TempDir(), key, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	f := &fake{onWA: map[string]types.JID{"15550000005": pn("15550000005")}}
	a := Actions{DB: db, Cli: f, Rows: rows()}
	if err := db.SaveChat(ctx, store.Chat{ID: "900002@lid", Name: "Alice", Kind: api.KindDirect, Phone: "15550000002"}); err != nil {
		t.Fatal(err)
	}
	return a, f
}

func TestSendText(t *testing.T) {
	ctx := context.Background()
	a, f := setup(t)

	// A known chat by phone number: sent to the LID.
	res, err := a.SendText(ctx, api.SendMessageInput{ChatID: "+15550000002", Text: "hi"})
	if err != nil || f.to[0].String() != "900002@lid" || f.sent[0].GetConversation() != "hi" || !strings.HasPrefix(res.MessageID, "900002@lid/") {
		t.Fatalf("send to a known chat: %+v %v to=%v", res, err, f.to)
	}
	// The sent message is stored.
	if got, _ := a.DB.Messages(ctx, store.Filter{Chat: "900002@lid", Limit: 5}); len(got) != 1 || !got[0].FromMe || got[0].Text != "hi" {
		t.Fatalf("the sent message is not stored: %+v", got)
	}

	// Unknown chat and unknown number.
	if _, err := a.SendText(ctx, api.SendMessageInput{ChatID: "999@g.us", Text: "x"}); api.CodeOf(err) != api.RecipientUnknown {
		t.Fatalf("unknown group: %v", err)
	}
	if _, err := a.SendText(ctx, api.SendMessageInput{ChatID: "+15550000009", Text: "x"}); api.CodeOf(err) != api.RecipientUnknown {
		t.Fatalf("number not on WhatsApp: %v", err)
	}
	// A new number that WhatsApp confirms.
	if _, err := a.SendText(ctx, api.SendMessageInput{ChatID: "+15550000005", Text: "x"}); err != nil {
		t.Fatalf("number on WhatsApp: %v", err)
	}

	// The same request ID gives the same WhatsApp ID.
	_, _ = a.SendText(ctx, api.SendMessageInput{ChatID: "900002@lid", Text: "once", RequestID: "r1"})
	_, _ = a.SendText(ctx, api.SendMessageInput{ChatID: "900002@lid", Text: "once", RequestID: "r1"})
	n := len(f.ids)
	if f.ids[n-1] != f.ids[n-2] || f.ids[n-1] == "" {
		t.Fatalf("request_id: ids %q and %q", f.ids[n-2], f.ids[n-1])
	}

	// Empty text.
	if _, err := a.SendText(ctx, api.SendMessageInput{ChatID: "900002@lid", Text: "  "}); api.CodeOf(err) != api.InvalidArgument {
		t.Fatalf("empty text: %v", err)
	}
}

func TestSendQuoteAndTimer(t *testing.T) {
	ctx := context.Background()
	a, f := setup(t)
	target := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("question"),
		ContextInfo: &waE2E.ContextInfo{Expiration: proto.Uint32(86400)}}}
	raw, _ := proto.Marshal(target)
	if _, err := a.DB.SaveMessage(ctx, store.Row{Chat: "900002@lid", ID: "Q1", Sender: "900002@lid", Time: time.Now(), Text: "question", Raw: raw}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SendText(ctx, api.SendMessageInput{ChatID: "900002@lid", Text: "answer", QuoteMessageID: "900002@lid/Q1"}); err != nil {
		t.Fatal(err)
	}
	ci := f.sent[0].GetExtendedTextMessage().GetContextInfo()
	if ci.GetStanzaID() != "Q1" || ci.GetParticipant() != "900002@lid" || ci.GetQuotedMessage() == nil || ci.GetExpiration() != 86400 {
		t.Fatalf("quote context: %+v", ci)
	}
	// Without a quote, the timer comes from the newest message of the chat.
	if _, err := a.SendText(ctx, api.SendMessageInput{ChatID: "900002@lid", Text: "more"}); err != nil {
		t.Fatal(err)
	}
	if e := f.sent[1].GetExtendedTextMessage().GetContextInfo().GetExpiration(); e != 86400 {
		t.Fatalf("timer of the chat: %d, want 86400", e)
	}
	if _, err := a.SendText(ctx, api.SendMessageInput{ChatID: "900002@lid", Text: "x", QuoteMessageID: "900002@lid/GONE"}); api.CodeOf(err) != api.NotFound {
		t.Fatalf("quote of a missing message: %v", err)
	}
}

func TestSendFile(t *testing.T) {
	ctx := context.Background()
	a, f := setup(t)
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 100)...)
	if _, err := a.SendFile(ctx, "900002@lid", "photo.png", "look", "", png); err != nil {
		t.Fatal(err)
	}
	if f.uploads[0] != whatsmeow.MediaImage || f.sent[0].GetImageMessage().GetCaption() != "look" || f.sent[0].GetImageMessage().GetMimetype() != "image/png" {
		t.Fatalf("image: %v %+v", f.uploads, f.sent[0])
	}
	if _, err := a.SendFile(ctx, "900002@lid", "notes.pdf", "", "", []byte("%PDF-1.4 x")); err != nil {
		t.Fatal(err)
	}
	d := f.sent[1].GetDocumentMessage()
	if f.uploads[1] != whatsmeow.MediaDocument || d.GetFileName() != "notes.pdf" || d.Caption != nil {
		t.Fatalf("document: %+v", d)
	}
}

func TestDownload(t *testing.T) {
	ctx := context.Background()
	a, f := setup(t)
	doc := &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("a.pdf"), Mimetype: proto.String("application/pdf"),
		FileLength: proto.Uint64(3), DirectPath: proto.String("/d")}}
	raw, _ := proto.Marshal(doc)
	text, _ := proto.Marshal(&waE2E.Message{Conversation: proto.String("x")})
	_, _ = a.DB.SaveMessage(ctx, store.Row{Chat: "900002@lid", ID: "F1", Sender: "900002@lid", Time: time.Now(), Text: "[document: a.pdf]", Raw: raw})
	_, _ = a.DB.SaveMessage(ctx, store.Row{Chat: "900002@lid", ID: "T1", Sender: "900002@lid", Time: time.Now(), Text: "x", Raw: text})

	f.download = []byte("abc")
	data, file, err := a.Download(ctx, "900002@lid/F1")
	if err != nil || string(data) != "abc" || file.Name != "a.pdf" || file.FileID != "900002@lid/F1" {
		t.Fatalf("download: %q %+v %v", data, file, err)
	}
	// The phone form of the chat gives the same file.
	if _, _, err := a.Download(ctx, "15550000002@s.whatsapp.net/F1"); err != nil {
		t.Fatalf("download with the phone form: %v", err)
	}
	for code, id := range map[api.Code]string{api.NoFile: "900002@lid/T1", api.NotFound: "900002@lid/NONE"} {
		if _, _, err := a.Download(ctx, id); api.CodeOf(err) != code {
			t.Errorf("Download(%s): %v, want %s", id, err, code)
		}
	}
	for _, status := range []int{403, 404, 410} {
		f.dlErr = whatsmeow.DownloadHTTPError{Response: &http.Response{StatusCode: status}}
		if _, _, err := a.Download(ctx, "900002@lid/F1"); api.CodeOf(err) != api.FileExpired {
			t.Errorf("HTTP %d: %v, want file_expired", status, err)
		}
	}
	f.dlErr = errors.New("network down")
	if _, _, err := a.Download(ctx, "900002@lid/F1"); api.CodeOf(err) == api.FileExpired {
		t.Error("a network error became file_expired")
	}
}
