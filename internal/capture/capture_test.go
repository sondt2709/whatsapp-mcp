package capture

import (
	"bytes"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"

	"github.com/sondt2709/whatsapp-mcp/internal/store"
)

func TestAnonymize(t *testing.T) {
	secret := []string{"Private text", "secret-plan.pdf", "Caption words", "15551239876", "777123", "ABCDEF123456", "QUOTEDID99", "Quoted words"}
	key := []byte("media-key-bytes-0123456789abcdef")
	m := &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
		FileName: proto.String("secret-plan.pdf"), Title: proto.String("secret-plan.pdf"), Caption: proto.String("Caption words"),
		Mimetype: proto.String("application/pdf"), MediaKey: key, JPEGThumbnail: key, DirectPath: proto.String("/v/t62/abc"),
		FileLength: proto.Uint64(1234),
		ContextInfo: &waE2E.ContextInfo{
			StanzaID: proto.String("QUOTEDID99"), Participant: proto.String("777123@lid"),
			MentionedJID:  []string{"15551239876@s.whatsapp.net"},
			QuotedMessage: &waE2E.Message{Conversation: proto.String("Quoted words")},
			Expiration:    proto.Uint32(86400),
		},
	}}
	raw, _ := proto.Marshal(m)
	a := New("15550009999", "555999")
	e, err := a.Event(store.Row{Chat: "15551239876@s.whatsapp.net", Sender: "15551239876@s.whatsapp.net", ID: "ABCDEF123456",
		Time: time.Now(), Text: "Private text", Raw: raw, SenderName: "Real Name"}, -time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	all := bytes.Join([][]byte{e.Message, []byte(e.Chat), []byte(e.Sender), []byte(e.ID), []byte(e.PushName)}, []byte("|"))
	for _, s := range append(secret, "Real Name") {
		if bytes.Contains(all, []byte(s)) {
			t.Errorf("the fixture still contains %q", s)
		}
	}
	if bytes.Contains(all, key) {
		t.Error("the fixture still contains the media key")
	}

	out := &waE2E.Message{}
	if err := proto.Unmarshal(e.Message, out); err != nil {
		t.Fatal(err)
	}
	d := out.GetDocumentMessage()
	ci := d.GetContextInfo()
	switch {
	case d.GetMimetype() != "application/pdf":
		t.Error("the MIME type changed")
	case d.GetFileLength() != 1234 || ci.GetExpiration() != 86400:
		t.Error("a number changed")
	case d.GetFileName() != "file-1.pdf" && d.GetFileName() != "file-2.pdf":
		t.Errorf("file name %q keeps no extension", d.GetFileName())
	case d.GetDirectPath() != "/expired" || d.MediaKey != nil || d.JPEGThumbnail != nil:
		t.Error("the file keys or the path stayed")
	case ci.GetQuotedMessage().GetConversation() == "" || ci.GetStanzaID() == "" || ci.GetParticipant() == "":
		t.Error("the quote lost its structure")
	case e.Chat != ci.GetMentionedJID()[0]:
		t.Errorf("one address maps to two fakes: %s and %s", e.Chat, ci.GetMentionedJID()[0])
	}

	// The own account maps to the fake account.
	if got := a.JID("15550009999:12@s.whatsapp.net"); got != "15550000001@s.whatsapp.net" {
		t.Errorf("own phone address maps to %s", got)
	}
	if got := a.JID("555999@lid"); got != "900001@lid" {
		t.Errorf("own LID maps to %s", got)
	}
}
