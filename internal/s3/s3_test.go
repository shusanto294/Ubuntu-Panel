package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// The two examples in AWS's Signature Version 4 documentation that sign
// exactly the headers this client signs.
func TestSignatureMatchesAWSExamples(t *testing.T) {
	client := New(Config{
		Endpoint:  "https://s3.amazonaws.com",
		Region:    "us-east-1",
		Bucket:    "examplebucket",
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	})
	client.now = func() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) }

	cases := []struct {
		name      string
		query     url.Values
		signature string
	}{
		{"lifecycle", url.Values{"lifecycle": {""}},
			"fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543"},
		{"list", url.Values{"max-keys": {"2"}, "prefix": {"J"}},
			"34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"},
	}

	for _, test := range cases {
		request, err := client.request(context.Background(), http.MethodGet, "", test.query, nil)
		if err != nil {
			t.Fatal(err)
		}

		if got := request.Header.Get("Authorization"); !strings.HasSuffix(got, "Signature="+test.signature) {
			t.Errorf("%s: wrong signature\n got %s\nwant %s", test.name, got, test.signature)
		}
	}
}

func TestEncode(t *testing.T) {
	if got := encodePath("/bucket/a b/ü+c~.txt"); got != "/bucket/a%20b/%C3%BC%2Bc~.txt" {
		t.Fatalf("got %s", got)
	}

	if got := canonicalQuery(url.Values{"prefix": {"a/b"}, "list-type": {"2"}}); got != "list-type=2&prefix=a%2Fb" {
		t.Fatalf("got %s", got)
	}
}

func TestConfig(t *testing.T) {
	config := Config{Endpoint: " s3.example.com/ ", Bucket: "my-bucket", Prefix: "/servers/one/",
		AccessKey: "a", SecretKey: "b"}.Normalise()

	if config.Endpoint != "https://s3.example.com" || config.Region != "us-east-1" || config.Prefix != "servers/one" {
		t.Fatalf("not normalised: %+v", config)
	}

	if problems := config.Validate(); len(problems) != 0 {
		t.Fatalf("valid config refused: %v", problems)
	}

	if key := config.Key("example.com", "/x/"); key != "servers/one/example.com/x" {
		t.Fatalf("key %q", key)
	}

	bad := Config{Endpoint: "ftp://x", Region: "US EAST", Bucket: "A", Prefix: "../x"}.Validate()
	for _, field := range []string{"endpoint", "region", "bucket", "prefix", "access_key", "secret_key"} {
		if bad[field] == "" {
			t.Errorf("%s should be refused", field)
		}
	}
}

// fakeS3 is enough of S3 to run the client against: objects, listings and
// multipart uploads, path-style.
type fakeS3 struct {
	sync.Mutex
	objects map[string][]byte
	uploads map[string]map[int][]byte
	parts   int
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.Lock()
	defer f.Unlock()

	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=key/") {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "<Error><Code>AccessDenied</Code><Message>no</Message></Error>")
		return
	}

	key := strings.TrimPrefix(r.URL.Path, "/bucket")
	key = strings.TrimPrefix(key, "/")
	query := r.URL.Query()
	body, _ := io.ReadAll(r.Body)

	switch {
	case r.Method == http.MethodGet && key == "":
		prefix := query.Get("prefix")
		keys := []string{}
		for k := range f.objects {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		fmt.Fprint(w, "<ListBucketResult>")
		for _, k := range keys {
			fmt.Fprintf(w, "<Contents><Key>%s</Key><Size>%d</Size></Contents>", k, len(f.objects[k]))
		}
		fmt.Fprint(w, "<IsTruncated>false</IsTruncated></ListBucketResult>")

	case r.Method == http.MethodPost && query.Has("uploads"):
		id := fmt.Sprintf("u%d", len(f.uploads)+1)
		f.uploads[id] = map[int][]byte{}
		fmt.Fprintf(w, "<InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>", id)

	case r.Method == http.MethodPut && query.Has("uploadId"):
		var number int
		fmt.Sscan(query.Get("partNumber"), &number)
		f.uploads[query.Get("uploadId")][number] = body
		f.parts++
		w.Header().Set("ETag", fmt.Sprintf(`"etag-%d"`, number))

	case r.Method == http.MethodPost && query.Has("uploadId"):
		var done struct {
			Parts []struct {
				Number int `xml:"PartNumber"`
			} `xml:"Part"`
		}
		xml.Unmarshal(body, &done)
		var whole []byte
		for _, part := range done.Parts {
			whole = append(whole, f.uploads[query.Get("uploadId")][part.Number]...)
		}
		f.objects[key] = whole
		fmt.Fprint(w, "<CompleteMultipartUploadResult/>")

	case r.Method == http.MethodPut:
		f.objects[key] = body

	case r.Method == http.MethodGet:
		data, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, "<Error><Code>NoSuchKey</Code></Error>")
			return
		}
		w.Write(data)

	case r.Method == http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	}
}

func TestAgainstFakeS3(t *testing.T) {
	fake := &fakeS3{objects: map[string][]byte{}, uploads: map[string]map[int][]byte{}}
	server := httptest.NewServer(fake)
	defer server.Close()

	client := New(Config{Endpoint: server.URL, Bucket: "bucket", Prefix: "p", AccessKey: "key",
		SecretKey: "secret", PathStyle: true})
	ctx := context.Background()

	if err := client.Check(ctx); err != nil {
		t.Fatalf("check: %v", err)
	}

	small := []byte("hello")
	if n, err := client.Upload(ctx, "p/site/small", bytes.NewReader(small), nil); err != nil || n != 5 {
		t.Fatalf("small upload: %d %v", n, err)
	}

	// Two and a half parts.
	large := bytes.Repeat([]byte("0123456789abcdef"), (partSize*5/2)/16)
	if n, err := client.Upload(ctx, "p/site/large", bytes.NewReader(large), nil); err != nil || n != int64(len(large)) {
		t.Fatalf("large upload: %d %v", n, err)
	}

	if fake.parts != 3 {
		t.Fatalf("want 3 parts, got %d", fake.parts)
	}

	var out bytes.Buffer
	if _, err := client.Download(ctx, "p/site/large", &out); err != nil || !bytes.Equal(out.Bytes(), large) {
		t.Fatalf("download differs: %v", err)
	}

	client.Put(ctx, "p/site-10/other", []byte("x"))

	objects, err := client.List(ctx, "p/site/")
	if err != nil || len(objects) != 2 {
		t.Fatalf("list: %v %v", objects, err)
	}

	if err := client.DeletePrefix(ctx, "p/site"); err != nil {
		t.Fatal(err)
	}

	if _, ok := fake.objects["p/site-10/other"]; !ok || len(fake.objects) != 1 {
		t.Fatalf("delete prefix took the wrong things: %v", fake.objects)
	}

	bad := New(Config{Endpoint: server.URL, Bucket: "bucket", AccessKey: "wrong", SecretKey: "s", PathStyle: true})
	if err := bad.Check(ctx); err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("want AccessDenied, got %v", err)
	}
}
