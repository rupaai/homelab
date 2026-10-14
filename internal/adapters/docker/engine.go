package docker

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// engine is a small client for the parts of the Docker Engine API Homehub needs. It talks
// to the socket directly so pulls can carry registry credentials without Homehub ever
// writing the user's ~/.docker/config.json.
type engine struct {
	base string // http://docker
	http *http.Client
}

func newEngine(host string) (*engine, error) {
	switch {
	case strings.HasPrefix(host, "unix://"):
		sock := strings.TrimPrefix(host, "unix://")
		return &engine{base: "http://docker", http: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "unix", sock)
			},
		}}}, nil
	case strings.HasPrefix(host, "tcp://"):
		return &engine{base: "http://" + strings.TrimPrefix(host, "tcp://"), http: &http.Client{}}, nil
	}
	return nil, fmt.Errorf("docker host %q is not supported (use unix:// or tcp://)", host)
}

// do sends one request and returns the response; the caller closes the body.
func (e *engine) do(ctx context.Context, method, path string, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, e.base+path, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("can't reach Docker (is it running?): %w", err)
	}
	return resp, nil
}

// getJSON GETs path and decodes the JSON answer into out. It returns core.ErrNotFound for 404.
func (e *engine) getJSON(ctx context.Context, path string, out any) error {
	resp, err := e.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return core.ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// apiError turns a failed API answer into an error that carries Docker's own message.
func apiError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var m struct {
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(b))
	if json.Unmarshal(b, &m) == nil && m.Message != "" {
		msg = m.Message
	}
	return fmt.Errorf("docker said %d: %s", resp.StatusCode, msg)
}

// hasImage reports whether ref (name@digest or name:tag) is already stored locally.
func (e *engine) hasImage(ctx context.Context, ref string) (bool, error) {
	var out struct{ ID string }
	err := e.getJSON(ctx, "/images/"+ref+"/json", &out)
	if err == core.ErrNotFound {
		return false, nil
	}
	return err == nil, err
}

// pull downloads ref, using auth when it has a username or password, and waits for the
// progress stream to end so a failure partway through is reported.
func (e *engine) pull(ctx context.Context, ref string, auth core.RegistryAuth) error {
	repo, tag := splitRef(ref)
	hdr := http.Header{"X-Registry-Auth": {encodeAuth(auth, repo)}}
	q := url.Values{"fromImage": {repo}, "tag": {tag}}
	resp, err := e.do(ctx, http.MethodPost, "/images/create?"+q.Encode(), hdr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}
	r := bufio.NewReader(resp.Body)
	dec := json.NewDecoder(r)
	for {
		var msg struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&msg); err == io.EOF {
			return nil
		} else if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("the download of %s was cut off: %w", ref, err)
		}
		if msg.Error != "" {
			return fmt.Errorf("can't download %s: %s", ref, msg.Error)
		}
		if msg.ErrorDetail.Message != "" {
			return fmt.Errorf("can't download %s: %s", ref, msg.ErrorDetail.Message)
		}
	}
}

// splitRef splits "repo@sha256:..." or "repo:tag" into the repository and the tag or digest.
func splitRef(ref string) (repo, tag string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

// encodeAuth builds the X-Registry-Auth header value. Empty credentials give "{}".
func encodeAuth(a core.RegistryAuth, repo string) string {
	if a.Username == "" && a.Password == "" {
		return base64.URLEncoding.EncodeToString([]byte("{}"))
	}
	server := a.Server
	if server == "" {
		server = repo
		if i := strings.Index(repo, "/"); i >= 0 {
			if first := repo[:i]; strings.ContainsAny(first, ".:") || first == "localhost" {
				server = first
			}
		}
	}
	b, _ := json.Marshal(map[string]string{"username": a.Username, "password": a.Password, "serveraddress": server})
	return base64.URLEncoding.EncodeToString(b)
}

// container is the part of GET /containers/json we use.
type container struct {
	ID      string            `json:"Id"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Created int64             `json:"Created"`
	Labels  map[string]string `json:"Labels"`
}

// containers lists every container (running or not) that carries the label filter.
func (e *engine) containers(ctx context.Context, labels ...string) ([]container, error) {
	f, _ := json.Marshal(map[string][]string{"label": labels})
	var out []container
	err := e.getJSON(ctx, "/containers/json?all=1&filters="+url.QueryEscape(string(f)), &out)
	return out, err
}

// startedAt returns when the container last started.
func (e *engine) startedAt(ctx context.Context, id string) (time.Time, error) {
	var out struct {
		State struct{ StartedAt string }
	}
	if err := e.getJSON(ctx, "/containers/"+id+"/json", &out); err != nil {
		return time.Time{}, err
	}
	t, err := time.Parse(time.RFC3339Nano, out.State.StartedAt)
	if err != nil || t.Year() < 2000 { // "0001-01-01..." means it never started
		return time.Time{}, nil
	}
	return t, nil
}
