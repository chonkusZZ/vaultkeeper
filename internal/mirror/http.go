package mirror

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ---------------- server ----------------

// Handler serves the mirror protocol under /mirror/v1/ for a destination agent.
// Authentication is done by the caller.
type Handler struct{ FS *FS }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	op := strings.TrimPrefix(r.URL.Path, "/mirror/v1/")
	q := r.URL.Query()
	dest, rel := q.Get("dest"), q.Get("path")
	fail := func(code int, err error) { http.Error(w, err.Error(), code) }
	e := Entry{P: rel, T: q.Get("t")}
	e.S, _ = strconv.ParseInt(q.Get("size"), 10, 64)
	e.M, _ = strconv.ParseInt(q.Get("mtime"), 10, 64)
	mode, _ := strconv.ParseUint(q.Get("mode"), 10, 32)
	e.O, e.L = uint32(mode), q.Get("target")
	if hv := r.Header.Get("X-VK-Meta"); hv != "" {
		var m Meta
		if raw, err := base64.StdEncoding.DecodeString(hv); err == nil && json.Unmarshal(raw, &m) == nil {
			e.Meta = &m
		}
	}
	// soft reports metadata that couldn't be applied while the data was written.
	soft := func(err error) bool {
		var mw *MetaWarning
		if errors.As(err, &mw) {
			w.Header().Set("X-VK-Warn", url.QueryEscape(mw.Msg))
			return true
		}
		return false
	}

	switch {
	case op == "info" && r.Method == http.MethodGet:
		if err := h.FS.Writable(dest); err != nil {
			fail(500, err)
		}
	case op == "manifest" && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "application/x-ndjson")
		bw := bufio.NewWriterSize(w, 64<<10)
		enc := json.NewEncoder(bw)
		cap := Capture{Owner: q.Get("owner") == "1", Names: q.Get("names") == "1", ACL: q.Get("acl") == "1"}
		err := h.FS.Manifest(r.Context(), dest, q["x"], cap, func(e Entry) error { return enc.Encode(e) })
		if err != nil {
			// No terminator: the client treats the manifest as incomplete.
			bw.Flush()
			panic(http.ErrAbortHandler)
		}
		_ = enc.Encode(Entry{T: "end"})
		bw.Flush()
	case op == "offset" && r.Method == http.MethodGet:
		n, err := h.FS.Offset(dest, rel, e.S, e.M)
		if err != nil {
			fail(400, err)
			return
		}
		fmt.Fprint(w, n)
	case op == "file" && r.Method == http.MethodPut:
		off, _ := strconv.ParseInt(q.Get("offset"), 10, 64)
		if err := h.FS.Put(dest, e, off, r.Body); err != nil && !soft(err) {
			code := 500
			if errors.Is(err, ErrOffset) {
				code = http.StatusConflict
			}
			fail(code, err)
		}
	case op == "mkdir" && r.Method == http.MethodPost:
		if err := h.FS.Mkdir(dest, e); err != nil {
			fail(500, err)
		}
	case op == "symlink" && r.Method == http.MethodPost:
		if err := h.FS.Symlink(dest, e); err != nil && !soft(err) {
			fail(500, err)
		}
	case op == "setmeta" && r.Method == http.MethodPost:
		var es []Entry
		if err := json.NewDecoder(r.Body).Decode(&es); err != nil {
			fail(400, err)
			return
		}
		if err := h.FS.SetMeta(dest, es); err != nil && !soft(err) {
			fail(500, err)
		}
	case op == "hash" && r.Method == http.MethodGet:
		s, err := h.FS.Hash(dest, rel)
		if err != nil {
			fail(404, err)
			return
		}
		fmt.Fprint(w, s)
	case op == "delete" && r.Method == http.MethodPost:
		var rels []string
		if err := json.NewDecoder(r.Body).Decode(&rels); err != nil {
			fail(400, err)
			return
		}
		if err := h.FS.Delete(dest, rels); err != nil {
			fail(500, err)
		}
	case op == "cleanup" && r.Method == http.MethodPost:
		_ = h.FS.Cleanup(dest)
	default:
		http.NotFound(w, r)
	}
}

// ---------------- client ----------------

// Client talks to a destination agent's mirror endpoint.
type Client struct {
	Base string // https://host:8765
	Dest string
	Tok  string
	HTTP *http.Client
}

func NewClient(base, certPEM, token, dest string, conns int) (*Client, error) {
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if certPEM != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(certPEM)) {
			return nil, errors.New("invalid destination certificate")
		}
		tc.RootCAs = pool
	}
	tr := &http.Transport{TLSClientConfig: tc, MaxIdleConnsPerHost: conns + 2, ResponseHeaderTimeout: 15 * time.Minute, IdleConnTimeout: time.Minute}
	return &Client{Base: strings.TrimRight(base, "/"), Dest: dest, Tok: token, HTTP: &http.Client{Transport: tr}}, nil
}

func (c *Client) do(ctx context.Context, method, op string, q url.Values, body io.Reader, size int64, meta *Meta) (*http.Response, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("dest", c.Dest)
	req, err := http.NewRequestWithContext(ctx, method, c.Base+"/mirror/v1/"+op+"?"+q.Encode(), body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth("restic", c.Tok)
	if meta != nil {
		if b, err := json.Marshal(meta); err == nil && len(b) < 48<<10 {
			req.Header.Set("X-VK-Meta", base64.StdEncoding.EncodeToString(b))
		}
	}
	if body != nil {
		req.ContentLength = size
		if size == 0 {
			req.Body = http.NoBody
		}
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		msg := strings.TrimSpace(string(b))
		if resp.StatusCode == http.StatusConflict {
			return nil, fmt.Errorf("%w: %s", ErrOffset, msg)
		}
		return nil, fmt.Errorf("destination: %s: %s", resp.Status, msg)
	}
	return resp, nil
}

// warned converts a successful response carrying X-VK-Warn into a MetaWarning.
func warned(resp *http.Response) error {
	if w := resp.Header.Get("X-VK-Warn"); w != "" {
		msg, _ := url.QueryUnescape(w)
		return &MetaWarning{Msg: msg}
	}
	return nil
}

func (c *Client) simple(ctx context.Context, method, op string, q url.Values, meta *Meta) error {
	resp, err := c.do(ctx, method, op, q, nil, 0, meta)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return warned(resp)
}

func entryQuery(e Entry) url.Values {
	q := url.Values{}
	q.Set("path", e.P)
	q.Set("size", strconv.FormatInt(e.S, 10))
	q.Set("mtime", strconv.FormatInt(e.M, 10))
	q.Set("mode", strconv.FormatUint(uint64(e.O), 10))
	if e.L != "" {
		q.Set("target", e.L)
	}
	return q
}

func (c *Client) Info(ctx context.Context) error { return c.simple(ctx, "GET", "info", nil, nil) }

func (c *Client) Manifest(ctx context.Context, excludes []string, cap Capture, emit func(Entry) error) error {
	q := url.Values{}
	if cap.Owner {
		q.Set("owner", "1")
	}
	if cap.Names {
		q.Set("names", "1")
	}
	if cap.ACL {
		q.Set("acl", "1")
	}
	for _, x := range excludes {
		q.Add("x", x)
	}
	resp, err := c.do(ctx, "GET", "manifest", q, nil, 0, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(bufio.NewReaderSize(resp.Body, 64<<10))
	for {
		var e Entry
		if err := dec.Decode(&e); err != nil {
			if err == io.EOF {
				return errors.New("destination manifest ended without a terminator (incomplete)")
			}
			return fmt.Errorf("destination manifest: %w", err)
		}
		if e.T == "end" {
			return nil
		}
		if err := emit(e); err != nil {
			return err
		}
	}
}

func (c *Client) Offset(ctx context.Context, e Entry) (int64, error) {
	resp, err := c.do(ctx, "GET", "offset", entryQuery(e), nil, 0, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 32))
	return strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
}

func (c *Client) Put(ctx context.Context, e Entry, offset int64, body io.Reader) error {
	q := entryQuery(e)
	q.Set("offset", strconv.FormatInt(offset, 10))
	resp, err := c.do(ctx, "PUT", "file", q, body, e.S-offset, e.Meta)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return warned(resp)
}

func (c *Client) Mkdir(ctx context.Context, e Entry) error {
	return c.simple(ctx, "POST", "mkdir", entryQuery(e), e.Meta)
}
func (c *Client) Symlink(ctx context.Context, e Entry) error {
	return c.simple(ctx, "POST", "symlink", entryQuery(e), e.Meta)
}

func (c *Client) postJSON(ctx context.Context, op string, v any) error {
	b, _ := json.Marshal(v)
	resp, err := c.do(ctx, "POST", op, nil, strings.NewReader(string(b)), int64(len(b)), nil)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return warned(resp)
}

func (c *Client) SetMeta(ctx context.Context, es []Entry) error {
	return c.postJSON(ctx, "setmeta", es)
}
func (c *Client) Delete(ctx context.Context, rels []string) error {
	return c.postJSON(ctx, "delete", rels)
}
func (c *Client) Cleanup(ctx context.Context) error {
	return c.simple(ctx, "POST", "cleanup", nil, nil)
}

func (c *Client) Hash(ctx context.Context, rel string) (string, error) {
	resp, err := c.do(ctx, "GET", "hash", url.Values{"path": {rel}}, nil, 0, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 128))
	return strings.TrimSpace(string(b)), nil
}
