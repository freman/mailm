package imaputil

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/textproto"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

// MessageHeader holds the UID and parsed MIME headers for a message.
type MessageHeader struct {
	UID     uint32
	Headers textproto.MIMEHeader
	Flags   []string
}

// FullMessage holds everything needed to re-APPEND a message.
type FullMessage struct {
	UID          uint32
	Flags        []string
	InternalDate time.Time
	Body         []byte
}

// Client wraps go-imap's Client with helpers for this tool.
type Client struct {
	c *client.Client
}

// Dial connects to an IMAP server using the specified TLS mode:
//   - "ssl"      - direct TLS (IMAPS); typical for port 993
//   - "starttls" - plain connection upgraded via STARTTLS; typical for port 143
//   - "none"     - no TLS; only permitted when allowInsecure is true
//
// allowInsecure skips certificate verification for ssl/starttls, and permits
// the "none" mode. It should never be used in production.
func Dial(host string, port int, tlsMode string, allowInsecure bool) (*Client, error) {
	addr := fmt.Sprintf("%s:%d", host, port)
	tlsCfg := &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: allowInsecure,
	}

	var c *client.Client
	var err error

	switch tlsMode {
	case "ssl":
		c, err = client.DialTLS(addr, tlsCfg)
		if err != nil {
			return nil, fmt.Errorf("TLS connect to %s: %w", addr, err)
		}

	case "starttls":
		c, err = client.Dial(addr)
		if err != nil {
			return nil, fmt.Errorf("connecting to %s: %w", addr, err)
		}
		supported, err := c.SupportStartTLS()
		if err != nil {
			c.Logout()
			return nil, fmt.Errorf("checking STARTTLS support on %s: %w", addr, err)
		}
		if !supported {
			c.Logout()
			return nil, fmt.Errorf("%s does not advertise STARTTLS; use tls: ssl or tls: none (with allow_insecure)", addr)
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			c.Logout()
			return nil, fmt.Errorf("STARTTLS on %s: %w", addr, err)
		}

	case "none":
		if !allowInsecure {
			return nil, fmt.Errorf("tls: none requires allow_insecure: true")
		}
		c, err = client.Dial(addr)
		if err != nil {
			return nil, fmt.Errorf("connecting to %s: %w", addr, err)
		}

	default:
		return nil, fmt.Errorf("unknown tls mode %q; use ssl, starttls, or none", tlsMode)
	}

	return &Client{c: c}, nil
}

// Login authenticates to the IMAP server.
func (cl *Client) Login(user, password string) error {
	if err := cl.c.Login(user, password); err != nil {
		return fmt.Errorf("login as %s: %w", user, err)
	}
	return nil
}

// ListFolders returns all folder names matching the given IMAP LIST pattern.
// Use "*" for all folders, "INBOX/*" for subfolders of INBOX, etc.
func (cl *Client) ListFolders(pattern string) ([]string, error) {
	mailboxes := make(chan *imap.MailboxInfo, 20)
	done := make(chan error, 1)
	go func() {
		done <- cl.c.List("", pattern, mailboxes)
	}()

	var folders []string
	for m := range mailboxes {
		folders = append(folders, m.Name)
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("LIST %q: %w", pattern, err)
	}
	return folders, nil
}

// SelectFolder selects a mailbox and returns its UIDVALIDITY.
// readOnly should be false for the source when delete_source is true.
func (cl *Client) SelectFolder(name string, readOnly bool) (uint32, error) {
	mbox, err := cl.c.Select(name, readOnly)
	if err != nil {
		return 0, fmt.Errorf("SELECT %q: %w", name, err)
	}
	return mbox.UidValidity, nil
}

// EnsureFolder creates the mailbox if it does not exist.
func (cl *Client) EnsureFolder(name string) error {
	// Try to select it first to check existence.
	_, err := cl.c.Select(name, true)
	if err == nil {
		return nil // already exists
	}
	if err := cl.c.Create(name); err != nil {
		return fmt.Errorf("CREATE %q: %w", name, err)
	}
	return nil
}

// SearchUIDs returns all UIDs in the currently selected folder, optionally
// filtered by since/before dates (zero values mean no filter).
func (cl *Client) SearchUIDs(since, before time.Time) ([]uint32, error) {
	criteria := imap.NewSearchCriteria()
	if !since.IsZero() {
		criteria.Since = since
	}
	if !before.IsZero() {
		criteria.Before = before
	}
	uids, err := cl.c.UidSearch(criteria)
	if err != nil {
		return nil, fmt.Errorf("UID SEARCH: %w", err)
	}
	return uids, nil
}

var headerFields = []string{
	"To", "Cc", "Delivered-To", "X-Original-To", "Envelope-To", "X-Forwarded-To",
}

// FetchHeaders fetches only the relevant headers for a batch of UIDs.
func (cl *Client) FetchHeaders(uids []uint32) ([]*MessageHeader, error) {
	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uids...)

	section := &imap.BodySectionName{
		BodyPartName: imap.BodyPartName{
			Specifier: imap.HeaderSpecifier,
			Fields:    headerFields,
		},
		Peek: true,
	}
	fetchItems := []imap.FetchItem{imap.FetchUid, imap.FetchFlags, section.FetchItem()}

	messages := make(chan *imap.Message, 20)
	done := make(chan error, 1)
	go func() {
		done <- cl.c.UidFetch(seqSet, fetchItems, messages)
	}()

	var results []*MessageHeader
	for msg := range messages {
		mh := &MessageHeader{
			UID:   msg.Uid,
			Flags: msg.Flags,
		}
		if r := msg.GetBody(section); r != nil {
			data, err := io.ReadAll(r)
			if err == nil {
				tp := textproto.NewReader(bufio.NewReader(bytes.NewReader(data)))
				hdr, _ := tp.ReadMIMEHeader()
				mh.Headers = hdr
			}
		}
		results = append(results, mh)
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("UID FETCH (headers): %w", err)
	}
	return results, nil
}

// FetchFull fetches the complete RFC 822 message, flags, and internal date for a batch of UIDs.
func (cl *Client) FetchFull(uids []uint32) ([]*FullMessage, error) {
	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uids...)

	section := &imap.BodySectionName{Peek: true}
	fetchItems := []imap.FetchItem{
		imap.FetchUid,
		imap.FetchFlags,
		imap.FetchInternalDate,
		section.FetchItem(),
	}

	messages := make(chan *imap.Message, 10)
	done := make(chan error, 1)
	go func() {
		done <- cl.c.UidFetch(seqSet, fetchItems, messages)
	}()

	var results []*FullMessage
	for msg := range messages {
		fm := &FullMessage{
			UID:          msg.Uid,
			Flags:        msg.Flags,
			InternalDate: msg.InternalDate,
		}
		if r := msg.GetBody(section); r != nil {
			data, err := io.ReadAll(r)
			if err == nil {
				fm.Body = data
			}
		}
		results = append(results, fm)
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("UID FETCH (full): %w", err)
	}
	return results, nil
}

// Append writes a message to the given folder, preserving flags and internal date.
func (cl *Client) Append(folder string, flags []string, date time.Time, body []byte) error {
	// Filter out \Recent - servers set this themselves.
	filtered := flags[:0:0]
	for _, f := range flags {
		if f != imap.RecentFlag {
			filtered = append(filtered, f)
		}
	}
	if err := cl.c.Append(folder, filtered, date, bytes.NewReader(body)); err != nil {
		return fmt.Errorf("APPEND to %q: %w", folder, err)
	}
	return nil
}

// MarkDeleted sets the \Deleted flag on a set of UIDs in the currently selected folder.
func (cl *Client) MarkDeleted(uids []uint32) error {
	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uids...)
	item := imap.FormatFlagsOp(imap.SetFlags, true)
	flags := []interface{}{imap.DeletedFlag}
	if err := cl.c.UidStore(seqSet, item, flags, nil); err != nil {
		return fmt.Errorf("UID STORE \\Deleted: %w", err)
	}
	return nil
}

// Expunge removes all messages marked \Deleted in the current folder.
func (cl *Client) Expunge() error {
	if err := cl.c.Expunge(nil); err != nil {
		return fmt.Errorf("EXPUNGE: %w", err)
	}
	return nil
}

// Logout closes the connection cleanly.
func (cl *Client) Logout() {
	_ = cl.c.Logout()
}
