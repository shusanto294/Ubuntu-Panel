// Package s3 talks to S3-compatible object storage: AWS S3, Cloudflare R2,
// Backblaze B2, Wasabi, DigitalOcean Spaces, MinIO and the rest. It is what
// remote backups are uploaded to and downloaded from.
//
// Written against the standard library rather than an SDK. The panel needs six
// requests — put, get, delete, list and the three halves of a multipart
// upload — signed with Signature Version 4, which every one of those services
// accepts. An SDK would be most of a megabyte of binary for that, and a
// dependency tree to keep up with.
package s3

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config is one bucket and the keys that reach it.
type Config struct {
	// Endpoint is the service's URL: https://s3.us-east-1.amazonaws.com,
	// https://<account>.r2.cloudflarestorage.com, https://s3.us-west-004.backblazeb2.com.
	Endpoint string `json:"endpoint"`

	// Region is what requests are signed for. "auto" for R2; whatever the
	// provider names otherwise; us-east-1 when it does not care.
	Region string `json:"region"`

	Bucket string `json:"bucket"`

	// Prefix is the folder inside the bucket everything goes under, so one
	// bucket can hold several servers' backups.
	Prefix string `json:"prefix"`

	AccessKey string `json:"access_key"`
	SecretKey string `json:"-"`

	// PathStyle puts the bucket in the path (endpoint/bucket/key) rather than
	// the hostname (bucket.endpoint/key). MinIO needs it; everything else
	// accepts it.
	PathStyle bool `json:"path_style"`
}

var (
	bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-_]{1,61}[a-z0-9]$`)
	regionPattern = regexp.MustCompile(`^[a-z0-9\-]{2,32}$`)
	prefixPattern = regexp.MustCompile(`^[A-Za-z0-9._\-/]*$`)
)

// Ready is whether there is enough here to try.
func (c Config) Ready() bool {
	return c.Endpoint != "" && c.Bucket != "" && c.AccessKey != "" && c.SecretKey != ""
}

// Normalise trims what a person pastes: spaces, a trailing slash, a prefix's
// leading and trailing slashes, and an endpoint typed without its scheme.
func (c Config) Normalise() Config {
	c.Endpoint = strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	if c.Endpoint != "" && !strings.Contains(c.Endpoint, "://") {
		c.Endpoint = "https://" + c.Endpoint
	}

	c.Region = strings.ToLower(strings.TrimSpace(c.Region))
	if c.Region == "" {
		c.Region = "us-east-1"
	}

	c.Bucket = strings.TrimSpace(c.Bucket)
	c.Prefix = strings.Trim(strings.TrimSpace(c.Prefix), "/")
	c.AccessKey = strings.TrimSpace(c.AccessKey)
	c.SecretKey = strings.TrimSpace(c.SecretKey)

	return c
}

// Validate is what the Settings form checks before anything is stored.
func (c Config) Validate() map[string]string {
	problems := map[string]string{}

	if endpoint, err := url.Parse(c.Endpoint); err != nil || endpoint.Host == "" ||
		(endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.Path != "" {
		problems["endpoint"] = "An https:// address with no path, like https://s3.us-east-1.amazonaws.com."
	}

	if !regionPattern.MatchString(c.Region) {
		problems["region"] = "A region like us-east-1, or auto for Cloudflare R2."
	}

	if !bucketPattern.MatchString(c.Bucket) {
		problems["bucket"] = "A bucket name: lower-case letters, digits, dots and dashes."
	}

	if !prefixPattern.MatchString(c.Prefix) || strings.Contains(c.Prefix, "..") || len(c.Prefix) > 200 {
		problems["prefix"] = "Letters, digits, dots, dashes, underscores and slashes only."
	}

	if c.AccessKey == "" {
		problems["access_key"] = "The access key ID is needed."
	}

	if c.SecretKey == "" {
		problems["secret_key"] = "The secret access key is needed."
	}

	return problems
}

// Key is a key under the configured prefix.
func (c Config) Key(parts ...string) string {
	all := []string{}

	if c.Prefix != "" {
		all = append(all, c.Prefix)
	}

	for _, part := range parts {
		if part = strings.Trim(part, "/"); part != "" {
			all = append(all, part)
		}
	}

	return strings.Join(all, "/")
}

// partSize is how much of an upload is held in memory and sent at a time. S3
// allows ten thousand parts, so this caps one file at 320 GB — well past any
// site the panel will be asked to back up — while costing 32 MB of memory.
const partSize = 32 << 20

// Client signs and sends requests for one Config.
type Client struct {
	config Config
	http   *http.Client

	// now is replaced by the tests.
	now func() time.Time
}

// New makes a client.
func New(config Config) *Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
		IdleConnTimeout:       90 * time.Second,
	}

	return &Client{config: config.Normalise(), http: &http.Client{Transport: transport}, now: time.Now}
}

// Config is what the client was made with.
func (c *Client) Config() Config { return c.config }

// Check proves the keys can write to and delete from the bucket, by doing both
// to a small object.
func (c *Client) Check(ctx context.Context) error {
	key := c.config.Key(".ubuntu-panel-check")

	if err := c.Put(ctx, key, []byte("Ubuntu Panel can write here.\n")); err != nil {
		return err
	}

	return c.Delete(ctx, key)
}

// Put uploads a small object in one request.
func (c *Client) Put(ctx context.Context, key string, body []byte) error {
	response, err := c.do(ctx, http.MethodPut, key, nil, body)
	if err != nil {
		return err
	}

	response.Body.Close()

	return nil
}

// Upload sends everything r has to key, in parts once it is larger than one.
// It returns how many bytes went. progress, when not nil, is told the running
// total after every part.
func (c *Client) Upload(ctx context.Context, key string, r io.Reader, progress func(int64)) (int64, error) {
	buffer := make([]byte, partSize)

	n, err := io.ReadFull(r, buffer)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		if err := c.Put(ctx, key, buffer[:n]); err != nil {
			return 0, err
		}

		if progress != nil {
			progress(int64(n))
		}

		return int64(n), nil
	}

	if err != nil {
		return 0, err
	}

	uploadID, err := c.startMultipart(ctx, key)
	if err != nil {
		return 0, err
	}

	parts := []completedPart{}
	total := int64(0)

	for number := 1; ; number++ {
		etag, err := c.uploadPart(ctx, key, uploadID, number, buffer[:n])
		if err != nil {
			c.abortMultipart(key, uploadID)
			return total, err
		}

		parts = append(parts, completedPart{Number: number, ETag: etag})
		total += int64(n)

		if progress != nil {
			progress(total)
		}

		n, err = io.ReadFull(r, buffer)
		if err == io.EOF {
			break
		}

		if err != nil && err != io.ErrUnexpectedEOF {
			c.abortMultipart(key, uploadID)
			return total, err
		}

		if number+1 > 10000 {
			c.abortMultipart(key, uploadID)
			return total, errors.New("the file is too large for one upload")
		}
	}

	if err := c.completeMultipart(ctx, key, uploadID, parts); err != nil {
		c.abortMultipart(key, uploadID)
		return total, err
	}

	return total, nil
}

// Download writes the object at key to w, and returns how many bytes it wrote.
func (c *Client) Download(ctx context.Context, key string, w io.Writer) (int64, error) {
	response, err := c.do(ctx, http.MethodGet, key, nil, nil)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()

	written, err := io.Copy(w, response.Body)
	if err != nil {
		return written, fmt.Errorf("the download stopped after %d bytes: %w", written, err)
	}

	if response.ContentLength >= 0 && written != response.ContentLength {
		return written, fmt.Errorf("the download stopped after %d of %d bytes", written, response.ContentLength)
	}

	return written, nil
}

// Delete removes one object. One that is already gone is not an error.
func (c *Client) Delete(ctx context.Context, key string) error {
	response, err := c.do(ctx, http.MethodDelete, key, nil, nil)
	if err != nil {
		var problem *Error
		if errors.As(err, &problem) && problem.Status == http.StatusNotFound {
			return nil
		}

		return err
	}

	response.Body.Close()

	return nil
}

// Object is one entry of a listing.
type Object struct {
	Key  string
	Size int64
}

// List is every object whose key starts with prefix.
func (c *Client) List(ctx context.Context, prefix string) ([]Object, error) {
	objects := []Object{}
	token := ""

	for {
		query := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			query.Set("continuation-token", token)
		}

		response, err := c.do(ctx, http.MethodGet, "", query, nil)
		if err != nil {
			return nil, err
		}

		var result struct {
			Contents []struct {
				Key  string `xml:"Key"`
				Size int64  `xml:"Size"`
			} `xml:"Contents"`
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
		}

		err = xml.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&result)
		response.Body.Close()

		if err != nil {
			return nil, fmt.Errorf("could not read the bucket listing: %w", err)
		}

		for _, entry := range result.Contents {
			objects = append(objects, Object{Key: entry.Key, Size: entry.Size})
		}

		if !result.IsTruncated || result.NextContinuationToken == "" {
			return objects, nil
		}

		token = result.NextContinuationToken
	}
}

// DeletePrefix removes every object under prefix, which is a folder: the
// prefix is given a trailing slash so "site-1" does not also take "site-10".
func (c *Client) DeletePrefix(ctx context.Context, prefix string) error {
	prefix = strings.TrimRight(prefix, "/") + "/"
	if prefix == "/" {
		return errors.New("refusing to delete everything in the bucket")
	}

	objects, err := c.List(ctx, prefix)
	if err != nil {
		return err
	}

	for _, object := range objects {
		if err := c.Delete(ctx, object.Key); err != nil {
			return err
		}
	}

	return nil
}

// ------------------------------------------------------------- multipart

type completedPart struct {
	XMLName xml.Name `xml:"Part"`
	Number  int      `xml:"PartNumber"`
	ETag    string   `xml:"ETag"`
}

func (c *Client) startMultipart(ctx context.Context, key string) (string, error) {
	response, err := c.do(ctx, http.MethodPost, key, url.Values{"uploads": {""}}, nil)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	var result struct {
		UploadID string `xml:"UploadId"`
	}

	if err := xml.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil || result.UploadID == "" {
		return "", errors.New("the storage did not start a multipart upload")
	}

	return result.UploadID, nil
}

func (c *Client) uploadPart(ctx context.Context, key, uploadID string, number int, body []byte) (string, error) {
	query := url.Values{"partNumber": {strconv.Itoa(number)}, "uploadId": {uploadID}}

	response, err := c.do(ctx, http.MethodPut, key, query, body)
	if err != nil {
		return "", err
	}
	response.Body.Close()

	etag := response.Header.Get("ETag")
	if etag == "" {
		return "", fmt.Errorf("the storage did not acknowledge part %d", number)
	}

	return etag, nil
}

func (c *Client) completeMultipart(ctx context.Context, key, uploadID string, parts []completedPart) error {
	body, err := xml.Marshal(struct {
		XMLName xml.Name        `xml:"CompleteMultipartUpload"`
		Parts   []completedPart `xml:"Part"`
	}{Parts: parts})
	if err != nil {
		return err
	}

	response, err := c.do(ctx, http.MethodPost, key, url.Values{"uploadId": {uploadID}}, body)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	// S3 can answer 200 and put the failure in the body.
	reply, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if bytes.Contains(reply, []byte("<Error>")) {
		return parseError(response.StatusCode, reply)
	}

	return nil
}

// abortMultipart throws away the parts of an upload that failed, so they do
// not sit in the bucket being billed for. Best effort, on its own deadline: the
// context the upload ran on may be the reason it failed.
func (c *Client) abortMultipart(key, uploadID string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if response, err := c.do(ctx, http.MethodDelete, key, url.Values{"uploadId": {uploadID}}, nil); err == nil {
		response.Body.Close()
	}
}

// ------------------------------------------------------------- requests

// Error is what the storage said when it refused a request.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	switch {
	case e.Code != "" && e.Message != "":
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	case e.Code != "":
		return e.Code
	default:
		return fmt.Sprintf("the storage answered %d %s", e.Status, http.StatusText(e.Status))
	}
}

func parseError(status int, body []byte) error {
	var result struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}

	xml.Unmarshal(body, &result)

	return &Error{Status: status, Code: result.Code, Message: result.Message}
}

// do sends one signed request, retrying a few times when the network or the
// service fails rather than the request. A response that is returned has a 2xx
// status and an open body the caller closes.
func (c *Client) do(ctx context.Context, method, key string, query url.Values, body []byte) (*http.Response, error) {
	var last error

	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * time.Second):
			}
		}

		request, err := c.request(ctx, method, key, query, body)
		if err != nil {
			return nil, err
		}

		response, err := c.http.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			last = fmt.Errorf("could not reach the storage: %w", err)

			continue
		}

		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return response, nil
		}

		reply, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()

		last = parseError(response.StatusCode, reply)

		if response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests {
			return nil, last
		}
	}

	return nil, last
}

// request builds and signs one request.
func (c *Client) request(ctx context.Context, method, key string, query url.Values, body []byte) (*http.Request, error) {
	endpoint, err := url.Parse(c.config.Endpoint)
	if err != nil {
		return nil, err
	}

	host := endpoint.Host
	path := "/"

	if c.config.PathStyle {
		path += c.config.Bucket
		if key != "" {
			path += "/" + key
		}
	} else {
		host = c.config.Bucket + "." + endpoint.Host
		path += key
	}

	target := &url.URL{
		Scheme:   endpoint.Scheme,
		Host:     host,
		Path:     path,
		RawPath:  encodePath(path),
		RawQuery: canonicalQuery(query),
	}

	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	request.ContentLength = int64(len(body))
	if body == nil {
		request.Body = http.NoBody
		request.ContentLength = 0
	}

	c.sign(request, target, body)

	return request, nil
}

// sign adds Signature Version 4 headers.
func (c *Client) sign(request *http.Request, target *url.URL, body []byte) {
	now := c.now().UTC()
	stamp := now.Format("20060102T150405Z")
	day := now.Format("20060102")

	payload := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(payload[:])

	request.Header.Set("x-amz-date", stamp)
	request.Header.Set("x-amz-content-sha256", payloadHash)

	canonicalHeaders := "host:" + target.Host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + stamp + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"

	canonical := strings.Join([]string{
		request.Method,
		target.RawPath,
		target.RawQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := day + "/" + c.config.Region + "/s3/aws4_request"
	hashed := sha256.Sum256([]byte(canonical))

	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(hashed[:])

	key := hmacSHA256([]byte("AWS4"+c.config.SecretKey), day)
	key = hmacSHA256(key, c.config.Region)
	key = hmacSHA256(key, "s3")
	key = hmacSHA256(key, "aws4_request")

	signature := hex.EncodeToString(hmacSHA256(key, toSign))

	request.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.config.AccessKey, scope, signedHeaders, signature))
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))

	return mac.Sum(nil)
}

// encode is S3's URI encoding: everything but the unreserved characters is
// percent-encoded, and a slash only when slash is true.
func encode(text string, slash bool) string {
	var out strings.Builder

	for _, b := range []byte(text) {
		switch {
		case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9',
			b == '-', b == '_', b == '.', b == '~':
			out.WriteByte(b)
		case b == '/' && !slash:
			out.WriteByte(b)
		default:
			fmt.Fprintf(&out, "%%%02X", b)
		}
	}

	return out.String()
}

func encodePath(path string) string { return encode(path, false) }

// canonicalQuery is the query sorted by key and encoded the way it is signed,
// which is also the way it is sent.
func canonicalQuery(query url.Values) string {
	if len(query) == 0 {
		return ""
	}

	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	pairs := []string{}

	for _, key := range keys {
		values := append([]string{}, query[key]...)
		sort.Strings(values)

		for _, value := range values {
			pairs = append(pairs, encode(key, true)+"="+encode(value, true))
		}
	}

	return strings.Join(pairs, "&")
}
