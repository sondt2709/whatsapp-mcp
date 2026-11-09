// Command whatsapp-mcp links to a personal WhatsApp account as a linked
// device and gives AI agents the WhatsApp functions through MCP and a CLI.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/sondt2709/whatsapp-mcp/internal/api"
	"github.com/sondt2709/whatsapp-mcp/internal/client"
	"github.com/sondt2709/whatsapp-mcp/internal/daemon"
	"github.com/sondt2709/whatsapp-mcp/internal/files"
	"github.com/sondt2709/whatsapp-mcp/internal/mcpserver"
)

var version = "dev"

// defaultDataDir is $WHATSAPP_MCP_DATA_DIR, else the data folder of the system.
func defaultDataDir() string {
	if d := os.Getenv("WHATSAPP_MCP_DATA_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "whatsapp-mcp")
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, "whatsapp-mcp")
	}
	return filepath.Join(home, ".local", "share", "whatsapp-mcp")
}

type options struct {
	dataDir     string
	readOnly    bool
	retention   time.Duration
	sendDirs    []string
	downloadDir string
	asJSON      bool
}

func (o *options) daemonArgs() []string {
	args := []string{"--retention", o.retention.String()}
	if o.readOnly {
		args = append(args, "--read-only")
	}
	return args
}

func (o *options) client(ctx context.Context) (*client.Client, error) {
	abs, err := filepath.Abs(o.dataDir)
	if err != nil {
		return nil, err
	}
	o.dataDir = abs
	c := client.New(abs)
	c.DaemonArgs = o.daemonArgs()
	return c, c.Ensure(ctx)
}

func main() {
	o := &options{}
	root := &cobra.Command{
		Use:           "whatsapp-mcp",
		Short:         "WhatsApp for AI agents: an MCP server and a CLI on a linked device",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Long: `whatsapp-mcp links to your WhatsApp account as a linked device and gives AI agents
the WhatsApp functions through MCP (stdio) and this CLI.

Start with "whatsapp-mcp pair". Then add "whatsapp-mcp mcp" to your MCP client.
A daemon holds the connection; the other commands start it when it does not run.`,
	}
	root.PersistentFlags().StringVar(&o.dataDir, "data-dir", defaultDataDir(), "data folder (database, socket, log); env WHATSAPP_MCP_DATA_DIR")
	root.PersistentFlags().BoolVar(&o.readOnly, "read-only", false, "daemon: turn off sending (used when a command starts the daemon)")
	root.PersistentFlags().DurationVar(&o.retention, "retention", api.DefaultRetentionHr*time.Hour, "daemon: delete messages older than this (used when a command starts the daemon)")

	root.AddCommand(daemonCmd(o), mcpCmd(o), pairCmd(o), unlockCmd(o), statusCmd(o), logoutCmd(o),
		chatsCmd(o), messagesCmd(o), waitCmd(o), sendCmd(o), sendFileCmd(o), downloadCmd(o))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		var e *api.Error
		if errors.As(err, &e) {
			fmt.Fprintf(os.Stderr, "error: %s: %s\n", e.Code, e.Message)
		} else {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(1)
	}
}

func daemonCmd(o *options) *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Hold the WhatsApp connection and serve the socket API (the other commands start it)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
			dir, err := filepath.Abs(o.dataDir)
			if err != nil {
				return err
			}
			return daemon.Run(cmd.Context(), daemon.Config{DataDir: dir, Retention: o.retention, ReadOnly: o.readOnly,
				Version: version, Keys: keyStore(), Backend: backend(dir), Log: log})
		},
	}
}

func mcpCmd(o *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run the MCP server on stdin and stdout",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := filepath.Abs(o.dataDir)
			if err != nil {
				return err
			}
			c := client.New(dir)
			c.DaemonArgs = o.daemonArgs()
			s, err := mcpserver.New(cmd.Context(), mcpserver.Options{Client: c, SendDirs: o.sendDirs, DownloadDir: o.downloadDir, Version: version})
			if err != nil {
				return err
			}
			return s.Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}
	addFileFlags(cmd, o)
	return cmd
}

func addFileFlags(cmd *cobra.Command, o *options) {
	cmd.Flags().StringArrayVar(&o.sendDirs, "send-dir", nil, "folder that send_file may read (repeat for more); default: the download folder")
	cmd.Flags().StringVar(&o.downloadDir, "download-dir", "", "default folder of download_file; default ~/Downloads")
}

// printJSON prints v as indented JSON.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func readPassphrase(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(b), err
}

func pairCmd(o *options) *cobra.Command {
	var phone string
	var passphrase bool
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Link this binary to your WhatsApp account (QR code, or a pairing code with --phone)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 4*time.Minute)
			defer cancel()
			c, err := o.client(ctx)
			if err != nil {
				return err
			}
			req := daemon.PairRequest{Phone: phone}
			if passphrase {
				p1, err := readPassphrase("New passphrase for the database key: ")
				if err != nil {
					return err
				}
				p2, err := readPassphrase("Type it again: ")
				if err != nil {
					return err
				}
				if p1 != p2 || len(p1) < 8 {
					return errors.New("the passphrases differ or have fewer than 8 characters")
				}
				req.Passphrase = p1
			}
			fmt.Fprintln(os.Stderr, "On the phone: WhatsApp > Settings > Linked devices > Link a device.")
			err = c.Pair(ctx, req, func(e daemon.PairEvent) {
				switch {
				case e.QR != "":
					q, qerr := qrText(e.QR)
					if qerr != nil {
						fmt.Fprintln(os.Stderr, "cannot draw the QR code:", qerr)
						return
					}
					fmt.Fprint(os.Stderr, "\x1b[2J\x1b[H")
					fmt.Fprintln(os.Stderr, "Scan this code with the phone (it changes about every 20 seconds):")
					fmt.Fprintln(os.Stderr, q)
				case e.Code != "":
					fmt.Fprintf(os.Stderr, "On the phone, choose \"Link with phone number instead\" and type this code: %s\n", e.Code)
				}
			})
			if err != nil {
				if api.CodeOf(err) == api.Locked && strings.Contains(err.Error(), "--passphrase") {
					return fmt.Errorf("%w\nThis system has no key store. Run \"whatsapp-mcp pair --passphrase\"", err)
				}
				return err
			}
			s, err := c.Status(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("paired as %s (%s). The phone lists the device as \"whatsapp-mcp\".\n", s.Phone, s.PushName)
			return nil
		},
	}
	cmd.Flags().StringVar(&phone, "phone", "", "your number in international format (+<country code><number>) to get a pairing code instead of a QR code")
	cmd.Flags().BoolVar(&passphrase, "passphrase", false, "derive the database key from a passphrase (for a system without a key store)")
	return cmd
}

func unlockCmd(o *options) *cobra.Command {
	return &cobra.Command{
		Use:   "unlock",
		Short: "Type the passphrase of the database key (only for a data folder paired with --passphrase)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			p, err := readPassphrase("Passphrase: ")
			if err != nil {
				return err
			}
			s, err := c.Unlock(cmd.Context(), p)
			if err != nil {
				return err
			}
			fmt.Println("state:", s.State)
			return nil
		},
	}
}

func statusCmd(o *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the state of the link and the account (tool get_status)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			s, err := c.Status(cmd.Context())
			if err != nil {
				return err
			}
			if o.asJSON {
				return printJSON(s)
			}
			fmt.Printf("state:     %s\n", s.State)
			if s.Phone != "" {
				fmt.Printf("account:   %s (%s)\n", s.Phone, s.PushName)
				fmt.Printf("user_id:   %s\n", s.UserID)
			}
			fmt.Printf("read-only: %v\n", s.ReadOnly)
			fmt.Printf("data:      %s\n", o.dataDir)
			if s.Action != "" {
				fmt.Printf("action:    %s\n", s.Action)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&o.asJSON, "json", false, "print the JSON of the MCP tool")
	return cmd
}

func logoutCmd(o *options) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Unlink the device on the phone and delete the database and its key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			res, err := c.Logout(cmd.Context())
			if err != nil {
				return err
			}
			if !res.Unlinked {
				fmt.Println(res.Message)
			}
			fmt.Println("logged out: the database and its key are deleted")
			return nil
		},
	}
}

func chatsCmd(o *options) *cobra.Command {
	var in api.ListChatsInput
	cmd := &cobra.Command{
		Use:   "list-chats",
		Short: "List chats, the most recent first (tool list_chats)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			out, err := c.ListChats(cmd.Context(), in)
			if err != nil {
				return err
			}
			if o.asJSON {
				return printJSON(out)
			}
			for _, ch := range out.Chats {
				last := ""
				if ch.LastMessageAt != nil {
					last = ch.LastMessageAt.Local().Format("2006-01-02 15:04")
				}
				fmt.Printf("%-16s %-6s %-36s %s\n", last, ch.Kind, ch.ChatID, ch.Name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&in.Query, "query", "", "match on the name or the phone number")
	cmd.Flags().StringVar(&in.Kind, "kind", "", "direct or group")
	cmd.Flags().IntVar(&in.Limit, "limit", api.DefaultLimit, "maximum number of chats")
	cmd.Flags().BoolVar(&o.asJSON, "json", false, "print the JSON of the MCP tool")
	return cmd
}

func printMessages(ms []api.Message) {
	for _, m := range ms {
		who := m.SenderName
		if m.FromMe {
			who = "me"
		}
		fmt.Printf("%s  %s  %s\n  %s: %s\n", m.Time.Local().Format("2006-01-02 15:04"), m.ChatName, m.MessageID, who, m.Text)
	}
}

func messagesCmd(o *options) *cobra.Command {
	var in api.ListMessagesInput
	cmd := &cobra.Command{
		Use:   "list-messages",
		Short: "Read or search stored messages, the newest first (tool list_messages)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			out, err := c.ListMessages(cmd.Context(), in)
			if err != nil {
				return err
			}
			if o.asJSON {
				return printJSON(out)
			}
			printMessages(out.Messages)
			fmt.Println("cursor:", out.Cursor)
			return nil
		},
	}
	cmd.Flags().StringVar(&in.ChatID, "chat", "", "chat_id, user_id or +<phone>; empty for all chats")
	cmd.Flags().StringVar(&in.Query, "query", "", "match on the text")
	cmd.Flags().StringVar(&in.Before, "before", "", "RFC 3339 time")
	cmd.Flags().StringVar(&in.After, "after", "", "RFC 3339 time")
	cmd.Flags().IntVar(&in.Limit, "limit", api.DefaultLimit, "maximum number of messages")
	cmd.Flags().BoolVar(&o.asJSON, "json", false, "print the JSON of the MCP tool")
	return cmd
}

func waitCmd(o *options) *cobra.Command {
	var in api.WaitInput
	cmd := &cobra.Command{
		Use:   "wait",
		Short: "Wait for new messages after a cursor (tool wait_for_messages)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			out, err := c.Wait(cmd.Context(), in)
			if err != nil {
				return err
			}
			if o.asJSON {
				return printJSON(out)
			}
			printMessages(out.Messages)
			fmt.Println("cursor:", out.Cursor)
			return nil
		},
	}
	cmd.Flags().StringVar(&in.ChatID, "chat", "", "only this chat")
	cmd.Flags().StringVar(&in.Cursor, "cursor", "", "cursor of an earlier result")
	cmd.Flags().IntVar(&in.TimeoutSeconds, "timeout", api.DefaultWait, "seconds to wait (maximum 600)")
	cmd.Flags().BoolVar(&o.asJSON, "json", false, "print the JSON of the MCP tool")
	return cmd
}

func sendCmd(o *options) *cobra.Command {
	var in api.SendMessageInput
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send a text message (tool send_message)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			out, err := c.Send(cmd.Context(), in)
			if err != nil {
				return err
			}
			if o.asJSON {
				return printJSON(out)
			}
			fmt.Println("sent:", out.MessageID)
			return nil
		},
	}
	cmd.Flags().StringVar(&in.ChatID, "chat", "", "chat_id, user_id or +<phone> (required)")
	cmd.Flags().StringVar(&in.Text, "text", "", "the text (required)")
	cmd.Flags().StringVar(&in.QuoteMessageID, "quote", "", "message_id to reply to")
	cmd.Flags().StringVar(&in.RequestID, "request-id", "", "same request ID: no second message")
	cmd.Flags().BoolVar(&o.asJSON, "json", false, "print the JSON of the MCP tool")
	_ = cmd.MarkFlagRequired("chat")
	_ = cmd.MarkFlagRequired("text")
	return cmd
}

func sendFileCmd(o *options) *cobra.Command {
	var in api.SendFileInput
	cmd := &cobra.Command{
		Use:   "send-file",
		Short: "Send a file from an allowed folder (tool send_file)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			dirs := o.sendDirs
			if len(dirs) == 0 {
				dirs = []string{downloadDir(o)}
			}
			if abs, err := filepath.Abs(in.Path); err == nil {
				in.Path = abs
			}
			out, err := mcpserver.SendFile(cmd.Context(), c, dirs, in)
			if err != nil {
				return err
			}
			if o.asJSON {
				return printJSON(out)
			}
			fmt.Println("sent:", out.MessageID)
			return nil
		},
	}
	cmd.Flags().StringVar(&in.ChatID, "chat", "", "chat_id, user_id or +<phone> (required)")
	cmd.Flags().StringVar(&in.Path, "path", "", "the file (required)")
	cmd.Flags().StringVar(&in.Caption, "caption", "", "text under the file")
	cmd.Flags().StringVar(&in.RequestID, "request-id", "", "same request ID: no second message")
	cmd.Flags().BoolVar(&o.asJSON, "json", false, "print the JSON of the MCP tool")
	addFileFlags(cmd, o)
	_ = cmd.MarkFlagRequired("chat")
	_ = cmd.MarkFlagRequired("path")
	return cmd
}

func downloadDir(o *options) string {
	if o.downloadDir != "" {
		return o.downloadDir
	}
	return files.DownloadDir()
}

func downloadCmd(o *options) *cobra.Command {
	var in api.DownloadInput
	cmd := &cobra.Command{
		Use:   "download",
		Short: "Save the file of a message (tool download_file)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := o.client(cmd.Context())
			if err != nil {
				return err
			}
			if in.Dir != "" {
				if abs, err := filepath.Abs(in.Dir); err == nil {
					in.Dir = abs
				}
			}
			out, err := mcpserver.Download(cmd.Context(), c, downloadDir(o), in)
			if err != nil {
				return err
			}
			if o.asJSON {
				return printJSON(out)
			}
			fmt.Println(out.Path)
			return nil
		},
	}
	cmd.Flags().StringVar(&in.FileID, "file", "", "file_id (required)")
	cmd.Flags().StringVar(&in.Dir, "dir", "", "existing folder; default ~/Downloads")
	cmd.Flags().BoolVar(&o.asJSON, "json", false, "print the JSON of the MCP tool")
	cmd.Flags().StringVar(&o.downloadDir, "download-dir", "", "same as --dir")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}
