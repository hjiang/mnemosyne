// Package imap provides a thin wrapper around the go-imap v2 client
// for use by the backup orchestrator.
package imap

import (
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
	"github.com/emersion/go-sasl"
	"golang.org/x/net/proxy"

	goiap "github.com/emersion/go-imap/v2"
)

// FolderInfo contains metadata returned by SELECT.
type FolderInfo struct {
	NumMessages uint32
	UIDValidity uint32
}

// Envelope contains the metadata for a single message.
type Envelope struct {
	UID       uint32
	MessageID string
	Subject   string
	From      string
	To        string
	Cc        string
	Date      int64
	Size      int64
}

// ProxyConfig holds optional SOCKS5 proxy settings for IMAP connections.
type ProxyConfig struct {
	Host     string
	Port     int
	Username string
	Password string
}

// Client wraps an authenticated IMAP connection.
type Client struct {
	raw *imapclient.Client
}

// dialTimeout is the default TCP dial timeout.
const dialTimeout = 30 * time.Second

// Dial connects to an IMAP server, authenticates, and returns a Client.
// Set useTLS to true for implicit TLS (port 993).
// If proxyConf is non-nil and has a non-empty Host, the connection is routed
// through a SOCKS5 proxy.
func Dial(addr, username, password string, useTLS bool, proxyConf *ProxyConfig) (*Client, error) {
	opts := &imapclient.Options{
		WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader},
	}

	var raw *imapclient.Client
	var err error

	switch {
	case proxyConf != nil && proxyConf.Host != "":
		raw, err = dialViaProxy(addr, useTLS, proxyConf, opts)
	case useTLS:
		raw, err = imapclient.DialTLS(addr, opts)
	default:
		raw, err = imapclient.DialInsecure(addr, opts)
	}
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", addr, err)
	}

	if err := raw.Login(username, password).Wait(); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("login: %w", err)
	}

	return &Client{raw: raw}, nil
}

// dialViaProxy establishes a connection through a SOCKS5 proxy.
func dialViaProxy(addr string, useTLS bool, pc *ProxyConfig, opts *imapclient.Options) (*imapclient.Client, error) {
	proxyAddr := net.JoinHostPort(pc.Host, fmt.Sprintf("%d", pc.Port))

	var auth *proxy.Auth
	if pc.Username != "" {
		auth = &proxy.Auth{User: pc.Username, Password: pc.Password}
	}

	dialer, err := proxy.SOCKS5("tcp", proxyAddr, auth, &net.Dialer{Timeout: dialTimeout})
	if err != nil {
		return nil, fmt.Errorf("creating SOCKS5 dialer: %w", err)
	}

	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dialing via proxy: %w", err)
	}

	if useTLS {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			conn.Close() //nolint:errcheck,gosec
			return nil, fmt.Errorf("parsing address for TLS: %w", err)
		}
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName: host,
			NextProtos: []string{"imap"},
		})
		if err := tlsConn.Handshake(); err != nil {
			tlsConn.Close() //nolint:errcheck,gosec
			return nil, fmt.Errorf("TLS handshake: %w", err)
		}
		conn = tlsConn
	}

	return imapclient.New(conn, opts), nil
}

// DialOAuth connects to an IMAP server and authenticates using OAUTHBEARER.
// Set tls to true for implicit TLS (port 993).
func DialOAuth(addr, username, accessToken string, useTLS bool) (*Client, error) {
	opts := &imapclient.Options{
		WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader},
	}
	var raw *imapclient.Client
	var err error
	if useTLS {
		raw, err = imapclient.DialTLS(addr, opts)
	} else {
		raw, err = imapclient.DialInsecure(addr, opts)
	}
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", addr, err)
	}

	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("parsing address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("parsing port in %q: %w", addr, err)
	}
	saslClient := sasl.NewOAuthBearerClient(&sasl.OAuthBearerOptions{
		Username: username,
		Token:    accessToken,
		Host:     host,
		Port:     port,
	})
	if err := raw.Authenticate(saslClient); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("oauth authenticate: %w", err)
	}

	return &Client{raw: raw}, nil
}

// Close logs out and closes the connection.
func (c *Client) Close() error {
	_ = c.raw.Logout().Wait()
	return c.raw.Close()
}

// ListFolders returns the names of all mailboxes.
func (c *Client) ListFolders() ([]string, error) {
	cmd := c.raw.List("", "*", nil)
	data, err := cmd.Collect()
	if err != nil {
		return nil, fmt.Errorf("listing folders: %w", err)
	}

	names := make([]string, len(data))
	for i, d := range data {
		names[i] = d.Mailbox
	}
	sort.Strings(names)
	return names, nil
}

// SelectFolder selects a mailbox and returns its metadata.
func (c *Client) SelectFolder(name string) (*FolderInfo, error) {
	data, err := c.raw.Select(name, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("selecting %q: %w", name, err)
	}
	return &FolderInfo{
		NumMessages: data.NumMessages,
		UIDValidity: data.UIDValidity,
	}, nil
}

// FetchEnvelopes fetches envelope metadata for UIDs in [startUID, endUID].
func (c *Client) FetchEnvelopes(startUID, endUID uint32) ([]Envelope, error) {
	uidSet := goiap.UIDSet{goiap.UIDRange{
		Start: goiap.UID(startUID),
		Stop:  goiap.UID(endUID),
	}}
	opts := &goiap.FetchOptions{
		UID:        true,
		Envelope:   true,
		RFC822Size: true,
	}

	bufs, err := c.raw.Fetch(uidSet, opts).Collect()

	// Process whatever was received, even on error (partial results).
	envs := make([]Envelope, 0, len(bufs))
	for _, buf := range bufs {
		env := Envelope{
			UID:  uint32(buf.UID),
			Size: buf.RFC822Size,
		}
		if buf.Envelope != nil {
			env.MessageID = buf.Envelope.MessageID
			env.Subject = buf.Envelope.Subject
			env.From = formatAddrs(buf.Envelope.From)
			env.To = formatAddrs(buf.Envelope.To)
			env.Cc = formatAddrs(buf.Envelope.Cc)
			if !buf.Envelope.Date.IsZero() {
				env.Date = buf.Envelope.Date.Unix()
			}
		}
		envs = append(envs, env)
	}

	sort.Slice(envs, func(i, j int) bool { return envs[i].UID < envs[j].UID })

	if err != nil {
		return envs, fmt.Errorf("fetching envelopes: %w", err)
	}
	return envs, nil
}

// FetchBody fetches the full RFC822 body of the message with the given UID.
func (c *Client) FetchBody(uid uint32) ([]byte, error) {
	uidSet := goiap.UIDSet{goiap.UIDRange{
		Start: goiap.UID(uid),
		Stop:  goiap.UID(uid),
	}}
	section := &goiap.FetchItemBodySection{Peek: true}
	opts := &goiap.FetchOptions{
		UID:         true,
		BodySection: []*goiap.FetchItemBodySection{section},
	}

	cmd := c.raw.Fetch(uidSet, opts)

	msg := cmd.Next()
	if msg == nil {
		// Surface connection errors instead of masking them as "no message".
		if err := cmd.Close(); err != nil {
			return nil, fmt.Errorf("fetching UID %d: %w", uid, err)
		}
		return nil, fmt.Errorf("no message with UID %d", uid)
	}

	buf, err := msg.Collect()
	if err != nil {
		cmd.Close() //nolint:errcheck,gosec
		return nil, fmt.Errorf("collecting message data: %w", err)
	}

	if err := cmd.Close(); err != nil {
		return nil, fmt.Errorf("fetching UID %d: %w", uid, err)
	}

	for _, bs := range buf.BodySection {
		return bs.Bytes, nil
	}
	return nil, fmt.Errorf("no body section in response for UID %d", uid)
}

// FetchBodies fetches full RFC822 bodies for multiple UIDs in a single IMAP
// FETCH command. It returns a map of UID→body for messages found, a slice of
// UIDs that were not returned by the server, and an error for connection-level
// failures. When err is non-nil, bodies may still contain partial results that
// were received before the failure.
func (c *Client) FetchBodies(uids []uint32) (map[uint32][]byte, []uint32, error) {
	if len(uids) == 0 {
		return nil, nil, nil
	}

	uidIDs := make([]goiap.UID, len(uids))
	for i, uid := range uids {
		uidIDs[i] = goiap.UID(uid)
	}
	uidSet := goiap.UIDSetNum(uidIDs...)

	section := &goiap.FetchItemBodySection{Peek: true}
	opts := &goiap.FetchOptions{
		UID:         true,
		BodySection: []*goiap.FetchItemBodySection{section},
	}

	bufs, err := c.raw.Fetch(uidSet, opts).Collect()

	bodies := make(map[uint32][]byte, len(bufs))
	for _, buf := range bufs {
		for _, bs := range buf.BodySection {
			bodies[uint32(buf.UID)] = bs.Bytes
			break
		}
	}

	var missing []uint32
	for _, uid := range uids {
		if _, ok := bodies[uid]; !ok {
			missing = append(missing, uid)
		}
	}

	if err != nil {
		return bodies, missing, fmt.Errorf("fetching bodies: %w", err)
	}
	return bodies, missing, nil
}

// MarkDeleted sets the \Deleted flag on the given UIDs.
func (c *Client) MarkDeleted(uids []uint32) error {
	if len(uids) == 0 {
		return nil
	}
	uidIDs := make([]goiap.UID, len(uids))
	for i, uid := range uids {
		uidIDs[i] = goiap.UID(uid)
	}
	set := goiap.UIDSetNum(uidIDs...)
	store := &goiap.StoreFlags{
		Op:     goiap.StoreFlagsAdd,
		Flags:  []goiap.Flag{goiap.FlagDeleted},
		Silent: true,
	}
	cmd := c.raw.Store(set, store, nil)
	return cmd.Close()
}

// Expunge permanently removes all messages marked \Deleted in the selected mailbox.
func (c *Client) Expunge() error {
	return c.raw.Expunge().Close()
}

func formatAddrs(addrs []goiap.Address) string {
	if len(addrs) == 0 {
		return ""
	}
	result := make([]string, 0, len(addrs))
	for _, a := range addrs {
		addr := a.Addr()
		if addr != "" {
			result = append(result, addr)
		}
	}
	return strings.Join(result, ", ")
}

