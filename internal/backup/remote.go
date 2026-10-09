package backup

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Remote is where backups go off the panel's host: a WebDAV folder or an S3 bucket.
type Remote interface {
	Put(ctx context.Context, name string, body io.Reader, size int64) error
	List(ctx context.Context) ([]Object, error)
	Get(ctx context.Context, name string) (io.ReadCloser, error)
	Delete(ctx context.Context, name string) error
}

// Object is a backup at a remote.
type Object struct {
	Name string    `json:"name"`
	Size int64     `json:"size"`
	Time time.Time `json:"time"`
}

// Client is how remotes are reached: HTTPS only, no redirects followed (they could carry the
// credentials elsewhere), and a long timeout for large backups.
var Client = &http.Client{Timeout: 2 * time.Hour, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// Ours says whether a remote object's name is one of the panel's backups.
func Ours(name string) bool {
	return strings.HasPrefix(name, "meridian-") && (strings.HasSuffix(name, ".zip.age") || strings.HasSuffix(name, ".zip"))
}

// checkName refuses names that are not plain file names.
func checkName(name string) error {
	if name == "" || len(name) > 200 || strings.ContainsAny(name, "/\\\x00") || name == "." || name == ".." {
		return errors.New("not a backup's name")
	}
	return nil
}

func httpsBase(raw, what string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("the %s address must be https://host/... (HTTPS only, no user name or query in it)", what)
	}
	return u, nil
}

// status turns an answer that is not a success into a plain error.
func status(resp *http.Response, what string) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	msg := strings.Join(strings.Fields(string(b)), " ")
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%s: the user name or password (access key) was refused (%s)", what, resp.Status)
	case http.StatusNotFound:
		return fmt.Errorf("%s: not found (%s) - check the address, bucket or folder", what, resp.Status)
	}
	if msg != "" {
		return fmt.Errorf("%s: %s - %s", what, resp.Status, msg)
	}
	return fmt.Errorf("%s: %s", what, resp.Status)
}

// ---------------------------------------------------------------- WebDAV

// WebDAV is a folder on a WebDAV server (Nextcloud, ownCloud, a NAS, Box, Koofr...).
type WebDAV struct {
	URL      string // the folder, https://
	User     string
	Password string
}

func (d *WebDAV) base() (*url.URL, error) {
	u, err := httpsBase(d.URL, "WebDAV")
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u, nil
}

func (d *WebDAV) do(ctx context.Context, method, name string, body io.Reader, size int64, hdr map[string]string) (*http.Response, error) {
	u, err := d.base()
	if err != nil {
		return nil, err
	}
	if name != "" {
		if err := checkName(name); err != nil {
			return nil, err
		}
		u = u.JoinPath(name)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if size >= 0 && body != nil {
		req.ContentLength = size
	}
	if d.User != "" || d.Password != "" {
		req.SetBasicAuth(d.User, d.Password)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := Client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("WebDAV cannot be reached: %w", err)
	}
	return resp, nil
}

func (d *WebDAV) Put(ctx context.Context, name string, body io.Reader, size int64) error {
	resp, err := d.do(ctx, http.MethodPut, name, body, size, map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return status(resp, "WebDAV upload")
}

func (d *WebDAV) List(ctx context.Context) ([]Object, error) {
	const q = `<?xml version="1.0" encoding="utf-8"?><d:propfind xmlns:d="DAV:"><d:prop><d:getcontentlength/><d:getlastmodified/><d:resourcetype/></d:prop></d:propfind>`
	resp, err := d.do(ctx, "PROPFIND", "", strings.NewReader(q), int64(len(q)), map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := status(resp, "WebDAV list"); err != nil {
		return nil, err
	}
	var ms struct {
		Responses []struct {
			Href  string `xml:"href"`
			Props []struct {
				Length   string `xml:"prop>getcontentlength"`
				Modified string `xml:"prop>getlastmodified"`
				Coll     *struct {
					Name xml.Name
				} `xml:"prop>resourcetype>collection"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&ms); err != nil {
		return nil, fmt.Errorf("WebDAV list: unexpected answer (%v)", err)
	}
	var out []Object
	for _, r := range ms.Responses {
		p := r.Href
		if u, err := url.Parse(p); err == nil {
			p = u.Path
		}
		name := p[strings.LastIndex(strings.TrimSuffix(p, "/"), "/")+1:]
		if dec, err := url.PathUnescape(name); err == nil {
			name = dec
		}
		if !Ours(name) {
			continue
		}
		o := Object{Name: name}
		for _, ps := range r.Props {
			if ps.Coll != nil {
				o.Name = ""
			}
			if n, err := strconv.ParseInt(strings.TrimSpace(ps.Length), 10, 64); err == nil {
				o.Size = n
			}
			if t, err := http.ParseTime(strings.TrimSpace(ps.Modified)); err == nil {
				o.Time = t
			}
		}
		if o.Name != "" {
			out = append(out, o)
		}
	}
	sortObjects(out)
	return out, nil
}

func (d *WebDAV) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	resp, err := d.do(ctx, http.MethodGet, name, nil, -1, nil)
	if err != nil {
		return nil, err
	}
	if err := status(resp, "WebDAV download"); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp.Body, nil
}

func (d *WebDAV) Delete(ctx context.Context, name string) error {
	resp, err := d.do(ctx, http.MethodDelete, name, nil, -1, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return status(resp, "WebDAV delete")
}

// ---------------------------------------------------------------- S3

// S3 is a bucket at an S3-compatible service (AWS S3, Cloudflare R2, Backblaze B2, Wasabi, MinIO...).
type S3 struct {
	Endpoint  string // https://s3.eu-central-1.amazonaws.com, https://<account>.r2.cloudflarestorage.com, ...
	Region    string // us-east-1 when the service has none (R2: auto)
	Bucket    string
	Prefix    string // a folder in the bucket, e.g. meridian/
	AccessKey string
	SecretKey string
	PathStyle bool // https://endpoint/bucket/key instead of https://bucket.endpoint/key
	now       func() time.Time
}

func (s *S3) key(name string) string { return strings.TrimPrefix(s.Prefix, "/") + name }

func (s *S3) objectURL(key string, q url.Values) (*url.URL, error) {
	u, err := httpsBase(s.Endpoint, "S3 endpoint")
	if err != nil {
		return nil, err
	}
	if s.Bucket == "" {
		return nil, errors.New("S3: the bucket is missing")
	}
	if s.PathStyle {
		u.Path = "/" + s.Bucket + "/" + key
	} else {
		u.Host = s.Bucket + "." + u.Host
		u.Path = "/" + key
	}
	u.RawQuery = q.Encode()
	return u, nil
}

func (s *S3) do(ctx context.Context, method, key string, q url.Values, body io.Reader, size int64) (*http.Response, error) {
	u, err := s.objectURL(key, q)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
	}
	payload := "UNSIGNED-PAYLOAD" // over HTTPS; the body need not be read twice
	if body == nil {
		payload = emptySHA256
	}
	t := time.Now
	if s.now != nil {
		t = s.now
	}
	s.sign(req, payload, t())
	resp, err := Client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("S3 cannot be reached: %w", err)
	}
	return resp, nil
}

const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// sign adds AWS Signature Version 4 to a request: the host, the x-amz-* headers and a Range header
// are signed.
func (s *S3) sign(req *http.Request, payload string, t time.Time) {
	t = t.UTC()
	amzDate, day := t.Format("20060102T150405Z"), t.Format("20060102")
	region := s.Region
	if region == "" {
		region = "us-east-1"
	}
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payload)
	headers := map[string]string{"host": req.URL.Host}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-amz-") || lk == "range" {
			headers[lk] = strings.TrimSpace(strings.Join(v, ","))
		}
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + headers[k] + "\n")
	}
	signed := strings.Join(names, ";")
	canon := strings.Join([]string{req.Method, awsEscapePath(req.URL.EscapedPath()), canonQuery(req.URL.Query()),
		canonHeaders.String(), signed, payload}, "\n")
	scope := day + "/" + region + "/s3/aws4_request"
	sum := sha256.Sum256([]byte(canon))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	k := hmacSHA256([]byte("AWS4"+s.SecretKey), day)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.AccessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// awsEscapePath is a path as S3 signs it: each segment percent-encoded (RFC 3986), slashes kept.
func awsEscapePath(p string) string {
	if p == "" {
		return "/"
	}
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		if dec, err := url.PathUnescape(seg); err == nil {
			seg = dec
		}
		segs[i] = awsEscape(seg)
	}
	return strings.Join(segs, "/")
}

func awsEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func canonQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, awsEscape(k)+"="+awsEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

func (s *S3) Put(ctx context.Context, name string, body io.Reader, size int64) error {
	if err := checkName(name); err != nil {
		return err
	}
	resp, err := s.do(ctx, http.MethodPut, s.key(name), nil, body, size)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return status(resp, "S3 upload")
}

func (s *S3) List(ctx context.Context) ([]Object, error) {
	var out []Object
	q := url.Values{"list-type": {"2"}, "prefix": {strings.TrimPrefix(s.Prefix, "/")}}
	for page := 0; page < 20; page++ {
		resp, err := s.do(ctx, http.MethodGet, "", q, nil, 0)
		if err != nil {
			return nil, err
		}
		if err := status(resp, "S3 list"); err != nil {
			resp.Body.Close()
			return nil, err
		}
		var r struct {
			Contents []struct {
				Key          string `xml:"Key"`
				Size         int64  `xml:"Size"`
				LastModified string `xml:"LastModified"`
			} `xml:"Contents"`
			Truncated bool   `xml:"IsTruncated"`
			Next      string `xml:"NextContinuationToken"`
		}
		err = xml.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&r)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("S3 list: unexpected answer (%v)", err)
		}
		for _, c := range r.Contents {
			name := strings.TrimPrefix(c.Key, strings.TrimPrefix(s.Prefix, "/"))
			if strings.Contains(name, "/") || !Ours(name) {
				continue
			}
			t, _ := time.Parse(time.RFC3339, c.LastModified)
			out = append(out, Object{Name: name, Size: c.Size, Time: t})
		}
		if !r.Truncated || r.Next == "" {
			break
		}
		q.Set("continuation-token", r.Next)
	}
	sortObjects(out)
	return out, nil
}

func (s *S3) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	resp, err := s.do(ctx, http.MethodGet, s.key(name), nil, nil, 0)
	if err != nil {
		return nil, err
	}
	if err := status(resp, "S3 download"); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp.Body, nil
}

func (s *S3) Delete(ctx context.Context, name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	resp, err := s.do(ctx, http.MethodDelete, s.key(name), nil, nil, 0)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return status(resp, "S3 delete")
}

// sortObjects puts the newest first.
func sortObjects(list []Object) {
	sort.Slice(list, func(i, j int) bool {
		if !list[i].Time.Equal(list[j].Time) {
			return list[i].Time.After(list[j].Time)
		}
		return list[i].Name > list[j].Name
	})
}
