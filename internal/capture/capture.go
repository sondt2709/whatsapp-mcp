// Package capture makes replay fixtures from stored messages. It keeps the
// structure of each real WhatsApp message (which fields are set, quotes,
// files, timers) and replaces all personal content: every address with a
// fake address, every ID with a fake ID, every other text with a placeholder.
// It removes all bytes (file keys, hashes, thumbnails) and shifts the times
// to a fixed date. Read the output before a commit.
package capture

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/sondt2709/whatsapp-mcp/internal/fakewa"
	"github.com/sondt2709/whatsapp-mcp/internal/store"
)

// Base is the time of the newest fixture message.
var Base = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

// keep are the string fields that hold no personal content.
var keep = map[protoreflect.Name]bool{"mimetype": true}

// idFields hold message IDs.
var idFields = map[protoreflect.Name]bool{"stanzaID": true, "ID": true}

var jidRe = regexp.MustCompile(`^[0-9A-Za-z.\-]+(:[0-9]+)?@(s\.whatsapp\.net|lid|g\.us)$`)

// Anonymizer maps real values to fake ones, the same fake value for the same
// real value.
type Anonymizer struct {
	users  map[string]string
	groups map[string]string
	ids    map[string]string
	texts  int
}

// New returns an empty anonymizer. The own account maps to the fake account
// of the fake backend, so the own chat stays the own chat.
func New(ownPN, ownLID string) *Anonymizer {
	a := &Anonymizer{users: map[string]string{}, groups: map[string]string{}, ids: map[string]string{}}
	if ownPN != "" {
		a.users[ownPN+"@s.whatsapp.net"] = fakewa.OwnPN.String()
	}
	if ownLID != "" {
		a.users[ownLID+"@lid"] = fakewa.OwnLID.String()
	}
	return a
}

// JID returns the fake address of a real address.
func (a *Anonymizer) JID(s string) string {
	if s == "" {
		return ""
	}
	user, server, _ := strings.Cut(s, "@")
	user, _, _ = strings.Cut(user, ":") // drop the device part
	key := user + "@" + server
	switch server {
	case "g.us":
		if v, ok := a.groups[key]; ok {
			return v
		}
		v := fmt.Sprintf("1203630000%08d@g.us", len(a.groups)+1)
		a.groups[key] = v
		return v
	case "s.whatsapp.net":
		if v, ok := a.users[key]; ok {
			return v
		}
		v := fmt.Sprintf("1555010%04d@s.whatsapp.net", len(a.users)+1)
		a.users[key] = v
		return v
	case "lid":
		if v, ok := a.users[key]; ok {
			return v
		}
		v := fmt.Sprintf("91%04d@lid", len(a.users)+1)
		a.users[key] = v
		return v
	}
	return "0@unknown"
}

// ID returns the fake ID of a real message ID.
func (a *Anonymizer) ID(s string) string {
	if s == "" {
		return ""
	}
	if v, ok := a.ids[s]; ok {
		return v
	}
	v := fmt.Sprintf("3EB0F1X7%012d", len(a.ids)+1)
	a.ids[s] = v
	return v
}

func (a *Anonymizer) text(field protoreflect.Name, s string) string {
	if s == "" {
		return ""
	}
	if field == "fileName" || field == "title" {
		ext := strings.ToLower(filepath.Ext(s))
		if len(ext) > 6 {
			ext = ""
		}
		a.texts++
		return fmt.Sprintf("file-%d%s", a.texts, ext)
	}
	a.texts++
	return fmt.Sprintf("text %d", a.texts)
}

// Message anonymizes a message in place.
func (a *Anonymizer) Message(m *waE2E.Message) { a.walk(m.ProtoReflect()) }

func (a *Anonymizer) walk(m protoreflect.Message) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsList():
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				switch fd.Kind() {
				case protoreflect.MessageKind, protoreflect.GroupKind:
					a.walk(l.Get(i).Message())
				case protoreflect.StringKind:
					l.Set(i, protoreflect.ValueOfString(a.str(fd, l.Get(i).String())))
				case protoreflect.BytesKind:
					l.Set(i, protoreflect.ValueOfBytes(nil))
				}
			}
		case fd.IsMap():
			m.Clear(fd)
		case fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind:
			a.walk(v.Message())
		case fd.Kind() == protoreflect.StringKind:
			m.Set(fd, protoreflect.ValueOfString(a.str(fd, v.String())))
		case fd.Kind() == protoreflect.BytesKind:
			m.Clear(fd)
		}
		return true
	})
}

func (a *Anonymizer) str(fd protoreflect.FieldDescriptor, s string) string {
	name := fd.Name()
	switch {
	case keep[name]:
		return s
	case jidRe.MatchString(s):
		return a.JID(s)
	case idFields[name]:
		return a.ID(s)
	case name == "directPath" || name == "URL" || name == "url":
		return "/expired"
	}
	return a.text(name, s)
}

// Event makes the fixture event of a stored row. shift moves the time.
func (a *Anonymizer) Event(r store.Row, shift time.Duration) (fakewa.Event, error) {
	m := &waE2E.Message{}
	if err := proto.Unmarshal(r.Raw, m); err != nil {
		return fakewa.Event{}, err
	}
	a.Message(m)
	raw, err := proto.Marshal(m)
	if err != nil {
		return fakewa.Event{}, err
	}
	e := fakewa.Event{Chat: a.JID(r.Chat), Sender: a.JID(r.Sender), FromMe: r.FromMe, ID: a.ID(r.ID),
		Time: r.Time.Add(shift).UnixMilli(), Message: raw}
	if !r.FromMe && r.SenderName != "" {
		e.PushName = "Person " + strings.TrimSuffix(strings.SplitN(e.Sender, "@", 2)[0], "")
	}
	return e, nil
}
