package whatsapp

import (
	"context"
	"strings"

	"go.mau.fi/whatsmeow/types"

	"github.com/sondt2709/whatsapp-mcp/internal/api"
)

// LIDMap finds the other address form of a user. whatsmeow's LID store
// implements it.
type LIDMap interface {
	GetPNForLID(ctx context.Context, lid types.JID) (types.JID, error)
	GetLIDForPN(ctx context.Context, pn types.JID) (types.JID, error)
}

// Resolver makes the canonical IDs. A user ID is the LID address when it is
// known, else the phone address. A direct chat has the user ID of the other
// person. A group keeps its address.
type Resolver struct {
	LIDs LIDMap
}

// User returns the canonical user ID of j. alt is the other address form
// when the event gives it (SenderAlt, RecipientAlt), else the zero JID.
func (r Resolver) User(ctx context.Context, j, alt types.JID) types.JID {
	j = j.ToNonAD()
	switch j.Server {
	case types.HiddenUserServer:
		return j
	case types.DefaultUserServer:
		if alt.Server == types.HiddenUserServer {
			return alt.ToNonAD()
		}
		if r.LIDs != nil {
			if lid, err := r.LIDs.GetLIDForPN(ctx, j); err == nil && !lid.IsEmpty() {
				return lid.ToNonAD()
			}
		}
	}
	return j
}

// Phone returns the phone number (digits) of a user ID, or "".
func (r Resolver) Phone(ctx context.Context, j, alt types.JID) string {
	j = j.ToNonAD()
	switch {
	case j.Server == types.DefaultUserServer:
		return j.User
	case alt.Server == types.DefaultUserServer:
		return alt.User
	case j.Server == types.HiddenUserServer && r.LIDs != nil:
		if pn, err := r.LIDs.GetPNForLID(ctx, j); err == nil && pn.Server == types.DefaultUserServer {
			return pn.User
		}
	}
	return ""
}

// ChatKind returns the kind of a chat address, and false for an address that
// whatsapp-mcp does not read: status updates, broadcast lists, Channels.
func ChatKind(j types.JID) (string, bool) {
	switch j.Server {
	case types.GroupServer:
		return api.KindGroup, true
	case types.DefaultUserServer, types.HiddenUserServer:
		if j.User == "" {
			return "", false
		}
		return api.KindDirect, true
	}
	return "", false
}

// ParseAddress parses a user or chat input: a canonical ID, the other
// address form, or "+<phone>". It does not resolve the canonical form.
func ParseAddress(s string) (types.JID, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return types.JID{}, api.Errorf(api.InvalidArgument, "the chat is empty")
	}
	if strings.HasPrefix(s, "+") || !strings.Contains(s, "@") {
		digits := strings.NewReplacer("+", "", " ", "", "-", "", "(", "", ")", "").Replace(s)
		if digits == "" || strings.Trim(digits, "0123456789") != "" {
			return types.JID{}, api.Errorf(api.InvalidArgument, "%q is not a chat ID or a phone number in the format +<country code><number>", s)
		}
		return types.NewJID(digits, types.DefaultUserServer), nil
	}
	j, err := types.ParseJID(s)
	if err != nil {
		return types.JID{}, api.Errorf(api.InvalidArgument, "%q is not a chat ID: %v", s, err)
	}
	if _, ok := ChatKind(j); !ok {
		return types.JID{}, api.Errorf(api.InvalidArgument, "%q is not a direct chat or a group", s)
	}
	return j.ToNonAD(), nil
}

// MessageID returns the canonical message ID.
func MessageID(chat, id string) string { return chat + "/" + id }

// SplitMessageID splits a message ID into the chat and the WhatsApp ID.
func SplitMessageID(s string) (chat, id string, err error) {
	i := strings.LastIndex(s, "/")
	if i <= 0 || i == len(s)-1 {
		return "", "", api.Errorf(api.InvalidArgument, "%q is not a message_id (<chat_id>/<id>)", s)
	}
	return s[:i], s[i+1:], nil
}
