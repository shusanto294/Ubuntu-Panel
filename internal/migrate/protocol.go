package migrate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// The receiving panel's endpoints. Public routes — the sending panel has no
// session there — that answer only a message sealed with a key the receiver
// issued.
const (
	PathHello    = "/migrate/api/hello"
	PathBegin    = "/migrate/api/begin"
	PathUpload   = "/migrate/api/upload"
	PathFinish   = "/migrate/api/finish"
	PathStatus   = "/migrate/api/status"
	PathFinalize = "/migrate/api/finalize"
	PathCatchUp  = "/migrate/api/catch-up"
	PathCancel   = "/migrate/api/cancel"
)

// KeyHeader names the key a request is sealed with.
const KeyHeader = "X-Migration-Key"

// ChunkSize is how much of a file one upload carries. Under a mebibyte with
// room for the seal, because nginx in front of every panel refuses a bigger
// body by default.
const ChunkSize = 768 * 1024

// Hello is what the receiving panel says about itself.
type Hello struct {
	Panel          string          `json:"panel"`
	Version        string          `json:"version"`
	PublicIP       string          `json:"public_ip"`
	Services       map[string]bool `json:"services"`
	MailConfigured bool            `json:"mail_configured"`
	SitesRoot      string          `json:"sites_root"`
	Sites          []string        `json:"sites"`
	MailDomains    []string        `json:"mail_domains"`
}

// Database is a site's database, password included: the site's own
// configuration names it, so the new server makes the same one.
type Database struct {
	Engine   string `json:"engine"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Password string `json:"password"`
	Charset  string `json:"charset"`
}

// DNSAccount is the credential a site's or a mail domain's DNS is managed
// with, so the new server can keep managing it.
type DNSAccount struct {
	Provider  string `json:"provider"`
	Label     string `json:"label"`
	APIToken  string `json:"api_token"`
	APISecret string `json:"api_secret"`
	Email     string `json:"email"`
	AccountID string `json:"account_id"`
}

// Site is everything about a site the new server needs to serve it the way
// the old one did. Its files and its database dump travel as files.
type Site struct {
	Domain       string   `json:"domain"`
	Type         string   `json:"type"`
	Aliases      []string `json:"aliases"`
	RootPath     string   `json:"root_path"`
	WebDirectory string   `json:"web_directory"`
	PHPVersion   string   `json:"php_version"`
	NodeVersion  string   `json:"node_version"`
	SSL          bool     `json:"ssl"`
	TLSMode      string   `json:"tls_mode"`
	AppPort      int      `json:"app_port"`
	StartCommand string   `json:"start_command"`
	BuildCommand string   `json:"build_command"`
	Repository   string   `json:"repository"`
	Branch       string   `json:"branch"`
	RepoAuth     string   `json:"repo_auth"`
	RepoToken    string   `json:"repo_token"`
	DeployKey    string   `json:"deploy_key"`
	DeployPublic string   `json:"deploy_public"`
	Webhook      string   `json:"webhook_secret"`
	WPAdminUser  string   `json:"wp_admin_user"`
	WPAdminPass  string   `json:"wp_admin_password"`
	WPAdminEmail string   `json:"wp_admin_email"`
	WPTitle      string   `json:"wp_title"`
	PHPSettings  string   `json:"php_settings"`
	CacheMode    string   `json:"cache_mode"`
	CacheTTL     string   `json:"cache_ttl"`
	ManageDNS    bool     `json:"manage_dns"`
	DNSType      string   `json:"dns_type"`
	DNSProxied   bool     `json:"dns_proxied"`

	Database *Database `json:"database,omitempty"`

	// Cron is the site's scheduled commands, which live outside its files.
	Cron []CronJob `json:"cron,omitempty"`
}

// CronJob is one of a site's scheduled commands.
type CronJob struct {
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
}

// Mailbox is one mailbox, password included: the new server's mailbox has to
// accept the same one, or every phone and mail client stops collecting.
type Mailbox struct {
	LocalPart string `json:"local_part"`
	Name      string `json:"name"`
	Password  string `json:"password"`
	QuotaMB   int    `json:"quota_mb"`
}

// Mail is a mail domain: its DKIM key, so mail signed before the move still
// verifies, and its mailboxes. The stored mail travels as a file.
type Mail struct {
	Domain       string    `json:"domain"`
	DKIMSelector string    `json:"dkim_selector"`
	DKIMPrivate  string    `json:"dkim_private"`
	DKIMPublic   string    `json:"dkim_public"`
	ManageDNS    bool      `json:"manage_dns"`
	Mailboxes    []Mailbox `json:"mailboxes"`
}

// Begin opens a transfer.
type Begin struct {
	Kind       string      `json:"kind"` // site | mail
	SourceIP   string      `json:"source_ip"`
	Site       *Site       `json:"site,omitempty"`
	Mail       *Mail       `json:"mail,omitempty"`
	DNSAccount *DNSAccount `json:"dns_account,omitempty"`
}

// Transfer names one transfer on the receiving panel.
type Transfer struct {
	Transfer string `json:"transfer"`
}

// Finish says the files are all there, with what each should hash to.
type Finish struct {
	Transfer string            `json:"transfer"`
	Files    map[string]string `json:"files"`
}

// Status is where a transfer has got to on the receiving panel.
type Status struct {
	Status  string `json:"status"` // receiving | importing | imported | finishing | done | failed
	Message string `json:"message"`
}

// Files are the names a transfer may upload, and nothing else: each is a path
// on the receiving server.
var Files = map[string]bool{
	"files.tar.gz":        true,
	"database.sql.gz":     true,
	"database.archive.gz": true,
	"mail.tar.gz":         true,
	"catch-up.tar.gz":     true,
}

// ------------------------------------------------------------------ client

// Client is the sending panel's side.
type Client struct {
	BaseURL string
	Key     Key
	HTTP    *http.Client
}

// NewClient checks the address and makes a client for it.
func NewClient(address string, key Key) (*Client, error) {
	address = strings.TrimRight(strings.TrimSpace(address), "/")
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}

	parsed, err := url.Parse(address)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("give the other panel's address, like https://panel.example.com or http://203.0.113.5:8443")
	}

	return &Client{
		BaseURL: parsed.Scheme + "://" + parsed.Host,
		Key:     key,
		HTTP: &http.Client{
			Timeout: 3 * time.Minute,
			// A redirect would turn every POST into a GET, and every message
			// is sealed for the address it was sent to.
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				return fmt.Errorf("the other panel's address redirects to %s://%s — use that address instead",
					req.URL.Scheme, req.URL.Host)
			},
		},
	}, nil
}

// Call sends a sealed request and opens the sealed answer.
func (c *Client) Call(path string, request, response any) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}

	answer, err := c.send(path, payload)
	if err != nil {
		return err
	}

	if response == nil {
		return nil
	}

	return json.Unmarshal(answer, response)
}

func (c *Client) send(pathAndQuery string, payload []byte) ([]byte, error) {
	sealed, err := c.Key.Seal("request "+pathAndQuery, payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, c.BaseURL+pathAndQuery, bytes.NewReader(sealed))
	if err != nil {
		return nil, err
	}

	req.Header.Set(KeyHeader, c.Key.ID)
	req.Header.Set("Content-Type", "application/octet-stream")

	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach the other panel: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("the other panel's answer was cut off: %w", err)
	}

	if res.StatusCode != http.StatusOK {
		return nil, remoteError(res.StatusCode, body)
	}

	answer, err := c.Key.Open("response "+pathAndQuery, body, nil)
	if err != nil {
		return nil, fmt.Errorf("the answer did not come from the panel that issued this key: %w", err)
	}

	var failure struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(answer, &failure) == nil && failure.Error != "" {
		return nil, errors.New(failure.Error)
	}

	return answer, nil
}

// remoteError is a refusal said in words. The receiver answers a key it does
// not know with plain text, since it has nothing to seal it with.
func remoteError(code int, body []byte) error {
	text := strings.TrimSpace(string(body))
	if len(text) > 300 || strings.Contains(text, "<html") {
		text = ""
	}

	switch {
	case code == http.StatusUnauthorized:
		return errors.New("the other panel does not recognise this migration key — it may have been replaced; issue a new one there")
	case code == http.StatusNotFound:
		return errors.New("the other panel does not answer migrations — update it to the latest version first")
	case code == http.StatusRequestEntityTooLarge:
		return errors.New("the other panel's nginx refused the upload as too large")
	case text != "":
		return fmt.Errorf("the other panel refused (%d): %s", code, text)
	}

	return fmt.Errorf("the other panel answered %d", code)
}

// Upload sends a file in chunks, and reports the hash of what it sent.
// progress, when given, is told how many bytes have gone.
func (c *Client) Upload(transfer, name, path string, progress func(sent, total int64)) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", err
	}

	hash := sha256.New()
	buffer := make([]byte, ChunkSize)
	offset := int64(0)

	for {
		n, readErr := io.ReadFull(file, buffer)
		if n > 0 {
			chunk := buffer[:n]
			hash.Write(chunk)

			query := url.Values{"transfer": {transfer}, "file": {name}, "offset": {strconv.FormatInt(offset, 10)}}
			target := PathUpload + "?" + query.Encode()

			var sendErr error
			for attempt := 0; attempt < 4; attempt++ {
				if _, sendErr = c.send(target, chunk); sendErr == nil {
					break
				}
				time.Sleep(time.Duration(attempt+1) * 3 * time.Second)
			}
			if sendErr != nil {
				return "", fmt.Errorf("uploading %s stopped at %d of %d bytes: %w", name, offset, info.Size(), sendErr)
			}

			offset += int64(n)
			if progress != nil {
				progress(offset, info.Size())
			}
		}

		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}

	// An empty file still has to exist on the other side.
	if offset == 0 {
		query := url.Values{"transfer": {transfer}, "file": {name}, "offset": {"0"}}
		if _, err := c.send(PathUpload+"?"+query.Encode(), nil); err != nil {
			return "", err
		}
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// FileHash is a file's SHA-256, for the receiver to check what arrived.
func FileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}
