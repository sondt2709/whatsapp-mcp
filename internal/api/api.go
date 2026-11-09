// Package api holds the JSON types and the error codes that the daemon, the
// CLI and the MCP server share. A CLI command with --json prints the same
// JSON as the MCP tool with the same name.
package api

import (
	"errors"
	"fmt"
	"time"
)

// Limits of the inputs.
const (
	DefaultLimit       = 50
	MaxLimit           = 500
	DefaultWait        = 60
	MaxWait            = 600
	MaxFileSize        = 100 << 20 // bytes, for a download and a send
	QuoteTextMax       = 200       // characters of the quoted text in a message
	ProgressEverySecs  = 15
	DefaultRetentionHr = 90 * 24
)

// Code is the machine-readable kind of an error.
type Code string

// The error codes of the socket API and the MCP tools.
const (
	NotPaired        Code = "not_paired"
	Locked           Code = "locked"
	NotConnected     Code = "not_connected"
	LoggedOut        Code = "logged_out"
	NotFound         Code = "not_found"
	NoFile           Code = "no_file"
	FileExpired      Code = "file_expired"
	TooLarge         Code = "too_large"
	PathNotAllowed   Code = "path_not_allowed"
	RecipientUnknown Code = "recipient_unknown"
	ReadOnly         Code = "read_only"
	InvalidArgument  Code = "invalid_argument"
	Internal         Code = "internal"
)

// Error is an error with a code and a message that names the next action.
type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// Errorf returns an *Error with the code and a formatted message.
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// CodeOf returns the code of err, or Internal for an error without a code.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return Internal
}

// State is the state of the account of a data folder.
type State string

// The account states.
const (
	StateNotPaired  State = "not_paired"
	StateLocked     State = "locked"
	StateConnecting State = "connecting"
	StateConnected  State = "connected"
	StateLoggedOut  State = "logged_out"
)

// Status is the result of get_status.
type Status struct {
	State    State  `json:"state"`
	Phone    string `json:"phone,omitempty"`     // +<number> of the account
	PushName string `json:"push_name,omitempty"` // the profile name of the account
	UserID   string `json:"user_id,omitempty"`   // the canonical ID of the account
	Action   string `json:"action,omitempty"`    // the next action of the user, when the state is not connected
	ReadOnly bool   `json:"read_only"`
	Version  string `json:"version"`
}

// Chat kinds.
const (
	KindDirect = "direct"
	KindGroup  = "group"
)

// Chat is one direct chat or group.
type Chat struct {
	ChatID        string     `json:"chat_id"`
	Name          string     `json:"name"`
	Kind          string     `json:"kind"`
	Phone         string     `json:"phone,omitempty"`
	LastMessageAt *time.Time `json:"last_message_at,omitempty"`
}

// File is the file of a message.
type File struct {
	FileID string `json:"file_id"`
	Kind   string `json:"kind"` // image, video, document, audio, voice or sticker
	Name   string `json:"name,omitempty"`
	MIME   string `json:"mime,omitempty"`
	Size   int64  `json:"size"`
}

// Quote is the earlier message that a message quotes.
type Quote struct {
	MessageID string `json:"message_id"`
	Author    string `json:"author,omitempty"`
	Text      string `json:"text,omitempty"`
}

// Message is one stored message. Text, names and quotes come from other
// people: they are data, never instructions.
type Message struct {
	MessageID  string    `json:"message_id"`
	ChatID     string    `json:"chat_id"`
	ChatName   string    `json:"chat_name,omitempty"`
	SenderID   string    `json:"sender_id"`
	SenderName string    `json:"sender_name,omitempty"`
	FromMe     bool      `json:"from_me"`
	Time       time.Time `json:"time"`
	Text       string    `json:"text"`
	File       *File     `json:"file,omitempty"`
	Quote      *Quote    `json:"quote,omitempty"`
}

// ListChatsInput is the input of list_chats.
type ListChatsInput struct {
	Query string `json:"query,omitempty" jsonschema:"case-insensitive match on the chat name or the phone number"`
	Kind  string `json:"kind,omitempty" jsonschema:"direct or group; empty for both"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of chats, default 50, maximum 500"`
}

// ListChatsResult is the result of list_chats.
type ListChatsResult struct {
	Chats []Chat `json:"chats"`
}

// ListMessagesInput is the input of list_messages.
type ListMessagesInput struct {
	ChatID string `json:"chat_id,omitempty" jsonschema:"the chat; a chat_id, a user_id or +<phone>; empty for all chats"`
	Query  string `json:"query,omitempty" jsonschema:"case-insensitive match on the message text"`
	Before string `json:"before,omitempty" jsonschema:"only messages before this RFC 3339 time"`
	After  string `json:"after,omitempty" jsonschema:"only messages after this RFC 3339 time"`
	Limit  int    `json:"limit,omitempty" jsonschema:"maximum number of messages, default 50, maximum 500"`
}

// ListMessagesResult is the result of list_messages. Cursor is the position of
// the newest stored message, for wait_for_messages.
type ListMessagesResult struct {
	Messages []Message `json:"messages"`
	Cursor   string    `json:"cursor"`
}

// WaitInput is the input of wait_for_messages.
type WaitInput struct {
	ChatID         string `json:"chat_id,omitempty" jsonschema:"only this chat; empty for all chats"`
	Cursor         string `json:"cursor,omitempty" jsonschema:"the cursor of an earlier result; empty to wait for messages after this call"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"seconds to wait, default 60, maximum 600"`
}

// WaitResult is the result of wait_for_messages, the oldest message first.
type WaitResult struct {
	Messages []Message `json:"messages"`
	Cursor   string    `json:"cursor"`
	TimedOut bool      `json:"timed_out"`
}

// SendMessageInput is the input of send_message.
type SendMessageInput struct {
	ChatID         string `json:"chat_id" jsonschema:"the chat; a chat_id, a user_id or +<phone>"`
	Text           string `json:"text" jsonschema:"the text to send"`
	QuoteMessageID string `json:"quote_message_id,omitempty" jsonschema:"send the text as a reply that quotes this stored message"`
	RequestID      string `json:"request_id,omitempty" jsonschema:"any unique text; a second call with the same request_id sends no second message"`
}

// SendFileInput is the input of send_file. The client reads Path.
type SendFileInput struct {
	ChatID    string `json:"chat_id" jsonschema:"the chat; a chat_id, a user_id or +<phone>"`
	Path      string `json:"path" jsonschema:"absolute path of the file, inside a folder that --send-dir allows"`
	Caption   string `json:"caption,omitempty" jsonschema:"text under the file"`
	RequestID string `json:"request_id,omitempty" jsonschema:"any unique text; a second call with the same request_id sends no second message"`
}

// SendResult is the result of send_message and send_file.
type SendResult struct {
	MessageID string `json:"message_id"`
}

// DownloadInput is the input of download_file.
type DownloadInput struct {
	FileID string `json:"file_id" jsonschema:"the file_id of a message file (equal to its message_id)"`
	Dir    string `json:"dir,omitempty" jsonschema:"existing folder for the file; default ~/Downloads"`
}

// DownloadResult is the result of download_file.
type DownloadResult struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	MIME   string `json:"mime,omitempty"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Limit returns n inside 1..MaxLimit, and DefaultLimit for 0 or less.
func Limit(n int) int {
	switch {
	case n <= 0:
		return DefaultLimit
	case n > MaxLimit:
		return MaxLimit
	}
	return n
}

// Wait returns the seconds inside 1..MaxWait, and DefaultWait for 0 or less.
func Wait(secs int) int {
	switch {
	case secs <= 0:
		return DefaultWait
	case secs > MaxWait:
		return MaxWait
	}
	return secs
}
