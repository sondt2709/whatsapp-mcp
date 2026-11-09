package whatsapp

import (
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// quoteMax limits the quoted text that a message keeps.
const quoteMax = 200

// Body returns the text of a message for the agent: the text, or a file
// placeholder with the caption. It returns "" for a message that has no
// text for the agent, for example a reaction, an edit, a delete or a poll vote.
func Body(m *waE2E.Message) string {
	if m == nil {
		return ""
	}
	with := func(tag, caption string) string {
		caption = strings.TrimSpace(caption)
		if caption == "" {
			return tag
		}
		return tag + " " + caption
	}
	switch {
	case m.GetConversation() != "":
		return strings.TrimSpace(m.GetConversation())
	case m.GetExtendedTextMessage() != nil:
		return strings.TrimSpace(m.GetExtendedTextMessage().GetText())
	case m.GetImageMessage() != nil:
		return with("[image]", m.GetImageMessage().GetCaption())
	case m.GetVideoMessage() != nil:
		return with("[video]", m.GetVideoMessage().GetCaption())
	case m.GetPtvMessage() != nil:
		return "[video message]"
	case m.GetDocumentMessage() != nil:
		d := m.GetDocumentMessage()
		name := d.GetFileName()
		if name == "" {
			name = d.GetTitle()
		}
		return with("[document: "+name+"]", d.GetCaption())
	case m.GetAudioMessage() != nil:
		if m.GetAudioMessage().GetPTT() {
			return "[voice message]"
		}
		return "[audio]"
	case m.GetStickerMessage() != nil:
		return "[sticker]"
	case m.GetContactMessage() != nil:
		return "[contact: " + m.GetContactMessage().GetDisplayName() + "]"
	case m.GetContactsArrayMessage() != nil:
		return "[contacts: " + m.GetContactsArrayMessage().GetDisplayName() + "]"
	case m.GetLocationMessage() != nil:
		l := m.GetLocationMessage()
		return "[location: " + strings.TrimSpace(l.GetName()+" "+l.GetAddress()) + "]"
	case m.GetLiveLocationMessage() != nil:
		return with("[live location]", m.GetLiveLocationMessage().GetCaption())
	case poll(m) != nil:
		return "[poll] " + poll(m).GetName()
	case m.GetEventMessage() != nil:
		return "[event] " + m.GetEventMessage().GetName()
	case m.GetGroupInviteMessage() != nil:
		return with("[group invite: "+m.GetGroupInviteMessage().GetGroupName()+"]", m.GetGroupInviteMessage().GetCaption())
	}
	return ""
}

func poll(m *waE2E.Message) *waE2E.PollCreationMessage {
	for _, p := range []*waE2E.PollCreationMessage{m.GetPollCreationMessage(), m.GetPollCreationMessageV2(),
		m.GetPollCreationMessageV3(), m.GetPollCreationMessageV5(), m.GetPollCreationMessageV6()} {
		if p != nil {
			return p
		}
	}
	return nil
}

// QuotedText returns at most quoteMax characters of the quoted message, on one line.
func QuotedText(m *waE2E.Message) string {
	ci := contextInfo(m)
	if ci == nil || ci.GetQuotedMessage() == nil {
		return ""
	}
	q := strings.Join(strings.Fields(Body(ci.GetQuotedMessage())), " ")
	if r := []rune(q); len(r) > quoteMax {
		q = string(r[:quoteMax]) + "…"
	}
	return q
}

// quoteRef returns the ID and the author of the message that m quotes, and
// false when m quotes no message. The author is the zero JID when the
// context does not name it.
func quoteRef(m *waE2E.Message) (string, types.JID, bool) {
	ci := contextInfo(m)
	if ci == nil || ci.GetQuotedMessage() == nil || ci.GetStanzaID() == "" {
		return "", types.JID{}, false
	}
	var who types.JID
	if p := ci.GetParticipant(); p != "" {
		if j, err := types.ParseJID(p); err == nil {
			who = j
		}
	}
	return ci.GetStanzaID(), who, true
}

type withContext interface {
	GetContextInfo() *waE2E.ContextInfo
}

// contextInfo returns the context of the message (mentions, quote, timer),
// or nil. The protobuf getters accept a nil receiver.
func contextInfo(m *waE2E.Message) *waE2E.ContextInfo {
	if m == nil {
		return nil
	}
	for _, c := range []withContext{m.GetExtendedTextMessage(), m.GetImageMessage(), m.GetVideoMessage(), m.GetPtvMessage(),
		m.GetDocumentMessage(), m.GetAudioMessage(), m.GetStickerMessage(), m.GetContactMessage(), m.GetLocationMessage(),
		m.GetLiveLocationMessage(), poll(m)} {
		if ci := c.GetContextInfo(); ci != nil {
			return ci
		}
	}
	return nil
}

// Media is the file of a message: the part that whatsmeow downloads, and the
// values that the sender gave.
type Media struct {
	Part   whatsmeow.DownloadableMessage
	Kind   string // image, video, document, audio, voice or sticker
	Name   string // the file name of a document; "" for the other kinds
	MIME   string
	Length uint64 // the size in bytes that the sender gave
}

// MediaOf returns the file of the message, or false for a message without a file.
func MediaOf(m *waE2E.Message) (Media, bool) {
	switch {
	case m.GetImageMessage() != nil:
		p := m.GetImageMessage()
		return Media{Part: p, Kind: "image", MIME: p.GetMimetype(), Length: p.GetFileLength()}, true
	case m.GetVideoMessage() != nil:
		p := m.GetVideoMessage()
		return Media{Part: p, Kind: "video", MIME: p.GetMimetype(), Length: p.GetFileLength()}, true
	case m.GetPtvMessage() != nil:
		p := m.GetPtvMessage()
		return Media{Part: p, Kind: "video", MIME: p.GetMimetype(), Length: p.GetFileLength()}, true
	case m.GetDocumentMessage() != nil:
		p := m.GetDocumentMessage()
		name := p.GetFileName()
		if name == "" {
			name = p.GetTitle()
		}
		return Media{Part: p, Kind: "document", Name: name, MIME: p.GetMimetype(), Length: p.GetFileLength()}, true
	case m.GetAudioMessage() != nil:
		p := m.GetAudioMessage()
		kind := "audio"
		if p.GetPTT() {
			kind = "voice"
		}
		return Media{Part: p, Kind: kind, MIME: p.GetMimetype(), Length: p.GetFileLength()}, true
	case m.GetStickerMessage() != nil:
		p := m.GetStickerMessage()
		return Media{Part: p, Kind: "sticker", MIME: p.GetMimetype(), Length: p.GetFileLength()}, true
	}
	return Media{}, false
}
