package whatsapp

import (
	"context"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sondt2709/whatsapp-mcp/internal/api"
	"github.com/sondt2709/whatsapp-mcp/internal/store"
)

// Me holds the addresses of the account. Either can be empty before pairing.
type Me struct {
	PN  types.JID // phone address, no device
	LID types.JID // LID address, no device
}

// ID returns the canonical user ID of the account.
func (me Me) ID() types.JID {
	if !me.LID.IsEmpty() {
		return me.LID
	}
	return me.PN
}

// Is reports whether j is an address of the account.
func (me Me) Is(j types.JID) bool {
	return j.User != "" && (j.User == me.PN.User && j.Server == types.DefaultUserServer ||
		j.User == me.LID.User && j.Server == types.HiddenUserServer)
}

// Names finds the contact name of an address.
type Names interface {
	Name(ctx context.Context, j types.JID) string
}

// Rows makes stored rows from WhatsApp events.
type Rows struct {
	Res   Resolver
	Me    Me
	Names Names
}

// chatID returns the canonical chat of a message. For a direct chat, the
// other address form comes from the sender (incoming) or the recipient (sent).
func (r Rows) chatID(ctx context.Context, info types.MessageInfo) types.JID {
	chat := info.Chat.ToNonAD()
	if chat.Server == types.GroupServer {
		return chat
	}
	if r.Me.Is(chat) {
		return r.Me.ID()
	}
	alt := info.SenderAlt
	if info.IsFromMe {
		alt = info.RecipientAlt
	}
	return r.Res.User(ctx, chat, alt)
}

// name returns the display name of a user: the contact name, the push name,
// else +<phone>.
func (r Rows) name(ctx context.Context, j, alt types.JID, push string) string {
	if r.Names != nil {
		for _, k := range []types.JID{j, alt} {
			if !k.IsEmpty() {
				if n := r.Names.Name(ctx, k.ToNonAD()); n != "" {
					return n
				}
			}
		}
	}
	if push != "" {
		return push
	}
	if p := r.Res.Phone(ctx, j, alt); p != "" {
		return "+" + p
	}
	return ""
}

// FromEvent makes the stored row of a message. It returns false for a
// message that whatsapp-mcp does not keep: an edit, a reaction, a delete, a
// poll vote, or a message of a status update, a broadcast list or a Channel.
func (r Rows) FromEvent(ctx context.Context, v *events.Message) (store.Row, bool) {
	if _, ok := ChatKind(v.Info.Chat); !ok || v.IsEdit || v.Message == nil {
		return store.Row{}, false
	}
	text := Body(v.Message)
	if text == "" {
		return store.Row{}, false
	}
	raw, err := proto.Marshal(v.Message)
	if err != nil {
		return store.Row{}, false
	}
	row := store.Row{
		Chat:   r.chatID(ctx, v.Info).String(),
		ID:     v.Info.ID,
		FromMe: v.Info.IsFromMe,
		Time:   v.Info.Timestamp,
		Text:   text,
		Raw:    raw,
	}
	if v.Info.IsFromMe {
		row.Sender = r.Me.ID().String()
	} else {
		row.Sender = r.Res.User(ctx, v.Info.Sender, v.Info.SenderAlt).String()
		row.SenderName = r.name(ctx, v.Info.Sender, v.Info.SenderAlt, v.Info.PushName)
	}
	if m, ok := MediaOf(v.Message); ok {
		row.MediaKind, row.MediaName, row.MediaMIME, row.MediaSize = m.Kind, m.Name, m.MIME, int64(m.Length)
	}
	if id, who, ok := quoteRef(v.Message); ok {
		row.QuoteID = id
		row.QuoteText = QuotedText(v.Message)
		switch {
		case r.Me.Is(who.ToNonAD()):
			row.QuoteAuth = "me"
		case !who.IsEmpty():
			row.QuoteAuth = r.name(ctx, who, types.JID{}, "")
		}
	}
	return row, true
}

// ChatOf returns the chat index entry of a stored row.
func (r Rows) ChatOf(ctx context.Context, v *events.Message, row store.Row) store.Chat {
	kind, _ := ChatKind(v.Info.Chat)
	c := store.Chat{ID: row.Chat, Kind: kind, Last: row.Time}
	if kind == api.KindDirect {
		alt := v.Info.SenderAlt
		if v.Info.IsFromMe {
			alt = v.Info.RecipientAlt
		}
		c.Phone = r.Res.Phone(ctx, v.Info.Chat, alt)
		// The contact name, else the push name of the other person. Never
		// "+<phone>", so that a weaker name does not replace a stored one.
		switch {
		case row.Chat == r.Me.ID().String():
			c.Name = "You"
		case r.Names != nil && r.Names.Name(ctx, v.Info.Chat.ToNonAD()) != "":
			c.Name = r.Names.Name(ctx, v.Info.Chat.ToNonAD())
		case r.Names != nil && !alt.IsEmpty() && r.Names.Name(ctx, alt.ToNonAD()) != "":
			c.Name = r.Names.Name(ctx, alt.ToNonAD())
		case !v.Info.IsFromMe:
			c.Name = v.Info.PushName
		}
	}
	return c
}
