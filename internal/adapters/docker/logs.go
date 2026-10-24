package docker

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rupaai/homelab/internal/core"
)

// Log follower: reads the logs of Homelab's app containers and hands them to a sink
// (Homelab stores them). It only reads: every call to Docker is a GET. It follows only
// containers that carry both homelab.app and homelab.env, with names that pass checkNames.
const (
	logPollEvery   = 5 * time.Second
	logFlushEvery  = time.Second
	logFlushLines  = 500
	logBufferLines = 10_000
	logWarnEvery   = time.Minute
	// logMaxLine is the longest line sent on (the log service cuts at 16 KB and adds "…").
	logMaxLine = 16<<10 + 1
	// logMaxFrame is the biggest frame we believe; a bigger header means the stream is broken.
	logMaxFrame = 4 << 20
	// logBackfill is how far back a container with no cursor is read: the logs are kept 14 days.
	logBackfill = 14 * 24 * time.Hour
	// logCursorKeep is how long the cursor of a container that is gone is kept.
	logCursorKeep = 7 * 24 * time.Hour
)

// LogSink takes the lines of one container. An error means "keep them and try again".
type LogSink func(ctx context.Context, b core.LogBatch) error

// LogFollower follows the logs of running app containers.
type LogFollower struct {
	api     *engine
	sink    LogSink
	log     *slog.Logger
	cursors *cursorFile

	poll, flush time.Duration
	maxBuffer   int
	now         func() time.Time

	mu         sync.Mutex
	active     map[string]bool // container id -> being followed
	lastWarned time.Time
}

// FollowLogs makes a follower that sends lines to sink and keeps its place in
// cursorFile (e.g. <home>/logs-cursors.json). Run it with Run.
func (d *Deployer) FollowLogs(sink LogSink, cursorFile string) *LogFollower {
	return newLogFollower(d.api, sink, cursorFile, d.log)
}

func newLogFollower(api *engine, sink LogSink, cursorPath string, log *slog.Logger) *LogFollower {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &LogFollower{api: api, sink: sink, log: log, cursors: openCursors(cursorPath, log),
		poll: logPollEvery, flush: logFlushEvery, maxBuffer: logBufferLines, now: time.Now, active: map[string]bool{}}
}

// Run follows until ctx ends, and returns once every container has stopped being followed.
func (f *LogFollower) Run(ctx context.Context) {
	var wg sync.WaitGroup
	t := time.NewTicker(f.poll)
	defer t.Stop()
	for {
		f.scan(ctx, &wg)
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-t.C:
		}
	}
}

// scan starts a follower for each running app container that has none.
func (f *LogFollower) scan(ctx context.Context, wg *sync.WaitGroup) {
	cs, err := f.running(ctx)
	if err != nil {
		if ctx.Err() == nil {
			f.warn("can't list the containers to read their logs", err)
		}
		return
	}
	alive := map[string]bool{}
	for _, c := range cs {
		alive[c.ID] = true
	}
	f.cursors.prune(f.now().Add(-logCursorKeep), alive)
	for _, c := range cs {
		app, env := c.Labels[labelApp], c.Labels[labelEnv]
		if app == "" || env == "" || checkNames(app, env) != nil {
			continue
		}
		service := c.Labels[labelService]
		if service == "" {
			service = serviceName
		}
		if !nameRe.MatchString(service) {
			continue
		}
		f.mu.Lock()
		busy := f.active[c.ID]
		f.active[c.ID] = true
		f.mu.Unlock()
		if busy {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { f.mu.Lock(); delete(f.active, c.ID); f.mu.Unlock() }()
			err := f.follow(ctx, c.ID, core.LogBatch{App: app, Env: env, Service: service})
			if err != nil && ctx.Err() == nil {
				f.warn("reading the logs of "+app+"/"+env+" stopped", err)
			}
		}()
	}
}

// running lists the running containers that have the Homelab labels.
func (f *LogFollower) running(ctx context.Context) ([]container, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {labelApp, labelEnv}, "status": {"running"}})
	var out []container
	err := f.api.getJSON(ctx, "/containers/json?filters="+url.QueryEscape(string(filters)), &out)
	return out, err
}

// warn logs a problem at most once a minute.
func (f *LogFollower) warn(msg string, err error) {
	f.mu.Lock()
	due := f.now().Sub(f.lastWarned) >= logWarnEvery
	if due {
		f.lastWarned = f.now()
	}
	f.mu.Unlock()
	if due {
		f.log.Warn(msg, "err", err)
	}
}

// logEntry is one line waiting to be sent.
type logEntry struct {
	ns   int64 // Docker's time, in unix nanoseconds
	line core.LogLine
}

// follow reads one container's log until it ends or ctx does, sending the lines in batches.
func (f *LogFollower) follow(ctx context.Context, id string, tmpl core.LogBatch) error {
	var info struct {
		Config struct{ Tty bool }
	}
	if err := f.api.getJSON(ctx, "/containers/"+id+"/json", &info); err != nil {
		return err
	}
	cursor := f.cursors.get(id)
	since := cursor
	if since == 0 {
		since = f.now().Add(-logBackfill).UnixNano()
	}
	q := url.Values{"follow": {"1"}, "stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"},
		"since": {fmt.Sprintf("%d.%09d", since/1e9, since%1e9)}}
	resp, err := f.api.do(ctx, http.MethodGet, "/containers/"+id+"/logs?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}

	skip := cursor // the reader's own copy: cursor moves while it reads
	lines := make(chan logEntry, 256)
	readErr := make(chan error, 1)
	rctx, stopRead := context.WithCancel(ctx)
	defer stopRead()
	go func() {
		defer close(lines)
		readErr <- readLogStream(resp.Body, info.Config.Tty, func(e logEntry) bool {
			if e.ns <= skip { // already sent
				return true
			}
			select {
			case lines <- e:
				return true
			case <-rctx.Done():
				return false
			}
		})
	}()

	var buf []logEntry
	// send tries to hand over the oldest lines; what the sink takes is gone from buf.
	send := func(c context.Context) {
		for len(buf) > 0 {
			n := min(len(buf), logFlushLines)
			b := tmpl
			b.Lines = make([]core.LogLine, n)
			var newest int64
			for i, e := range buf[:n] {
				b.Lines[i] = e.line
				newest = max(newest, e.ns)
			}
			if err := f.sink(c, b); err != nil {
				f.warn("can't store the logs of "+tmpl.App+"/"+tmpl.Env, err)
				return
			}
			buf = buf[n:]
			cursor = max(cursor, newest)
			f.cursors.set(id, cursor)
		}
	}
	tick := time.NewTicker(f.flush)
	defer tick.Stop()
	for {
		select {
		case e, ok := <-lines:
			if !ok {
				send(ctx)
				stopRead()
				if err := <-readErr; err != nil && ctx.Err() == nil && !errors.Is(err, io.EOF) {
					return err
				}
				return nil
			}
			buf = append(buf, e)
			if len(buf) > f.maxBuffer {
				over := len(buf) - f.maxBuffer
				buf = append(buf[:0], buf[over:]...)
				f.warn(fmt.Sprintf("dropping the oldest %d log lines of %s/%s: the log store is not taking them", over, tmpl.App, tmpl.Env), errors.New("buffer full"))
			}
			if len(buf) >= logFlushLines {
				send(ctx)
			}
		case <-tick.C:
			send(ctx)
		case <-ctx.Done():
			// A last try, so a stop does not lose what was just read.
			last, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			send(last)
			cancel()
			return nil
		}
	}
}

// readLogStream reads Docker's log stream and calls emit for each line. With a TTY the
// stream is raw text; without one it is frames: an 8-byte header (stream, 0, 0, 0, size as
// big-endian uint32) and the payload. A line may span frames, and a frame may hold several
// lines, so each stream keeps its own unfinished line. emit returns false to stop.
func readLogStream(r io.Reader, tty bool, emit func(logEntry) bool) error {
	br := bufio.NewReaderSize(r, 64<<10)
	if tty {
		return splitLines(br, "stdout", emit)
	}
	pending := map[string]*lineSplitter{"stdout": {stream: "stdout"}, "stderr": {stream: "stderr"}}
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
				return flushSplitters(pending, emit)
			}
			return err
		}
		size := binary.BigEndian.Uint32(hdr[4:])
		if size > logMaxFrame || hdr[1]|hdr[2]|hdr[3] != 0 {
			return errors.New("the log stream is not in Docker's frame format")
		}
		payload := make([]byte, size)
		if _, err := io.ReadFull(br, payload); err != nil {
			return err
		}
		stream := "stdout"
		switch hdr[0] {
		case 0, 1:
		case 2:
			stream = "stderr"
		default:
			continue
		}
		if !pending[stream].add(payload, emit) {
			return nil
		}
	}
}

func flushSplitters(m map[string]*lineSplitter, emit func(logEntry) bool) error {
	for _, s := range m {
		if !s.finish(emit) {
			return nil
		}
	}
	return nil
}

// splitLines reads raw text (a TTY) line by line.
func splitLines(r io.Reader, stream string, emit func(logEntry) bool) error {
	s := &lineSplitter{stream: stream}
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 && !s.add(buf[:n], emit) {
			return nil
		}
		if err != nil {
			if err == io.EOF {
				s.finish(emit)
				return nil
			}
			return err
		}
	}
}

// lineSplitter joins the pieces of one stream into lines.
type lineSplitter struct {
	stream string
	part   []byte
	last   int64 // the last line's time, for a piece with no time of its own
}

func (s *lineSplitter) add(p []byte, emit func(logEntry) bool) bool {
	for len(p) > 0 {
		i := indexByte(p, '\n')
		if i < 0 {
			s.part = append(s.part, p...)
			if len(s.part) > 2*logMaxLine { // a line with no end: send what there is
				ok := s.emitLine(s.part, emit)
				s.part = s.part[:0]
				return ok
			}
			return true
		}
		s.part = append(s.part, p[:i]...)
		ok := s.emitLine(s.part, emit)
		s.part = s.part[:0]
		if !ok {
			return false
		}
		p = p[i+1:]
	}
	return true
}

func (s *lineSplitter) finish(emit func(logEntry) bool) bool {
	if len(s.part) == 0 {
		return true
	}
	ok := s.emitLine(s.part, emit)
	s.part = s.part[:0]
	return ok
}

func indexByte(p []byte, c byte) int {
	for i, b := range p {
		if b == c {
			return i
		}
	}
	return -1
}

// emitLine reads the time Docker put in front of the line and passes the line on.
func (s *lineSplitter) emitLine(raw []byte, emit func(logEntry) bool) bool {
	text := strings.TrimSuffix(string(raw), "\r")
	ns := s.last
	if head, rest, ok := strings.Cut(text, " "); ok {
		if t, err := time.Parse(time.RFC3339Nano, head); err == nil {
			ns, text = t.UnixNano(), rest
		}
	}
	if ns == 0 {
		ns = time.Now().UnixNano()
	}
	s.last = ns
	if strings.TrimSpace(text) == "" {
		return true
	}
	if len(text) > logMaxLine {
		text = text[:logMaxLine]
	}
	return emit(logEntry{ns: ns, line: core.LogLine{T: ns / 1e6, Stream: s.stream, Text: text}})
}

// cursorFile keeps, for each container, the time of the last line that was stored.
type cursorFile struct {
	mu   sync.Mutex
	path string
	log  *slog.Logger
	m    map[string]int64 // container id -> unix nanoseconds
}

func openCursors(path string, log *slog.Logger) *cursorFile {
	c := &cursorFile{path: path, log: log, m: map[string]int64{}}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &c.m); err != nil {
			log.Warn("the log cursors file is damaged; reading the logs again from the start", "path", path, "err", err)
			c.m = map[string]int64{}
		}
	}
	return c
}

func (c *cursorFile) get(id string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[id]
}

func (c *cursorFile) set(id string, ns int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m[id] == ns {
		return
	}
	c.m[id] = ns
	c.save()
}

// prune forgets containers that are not running and whose last stored line is older than cutoff.
func (c *cursorFile) prune(cutoff time.Time, running map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	changed := false
	for id, ns := range c.m {
		if ns < cutoff.UnixNano() && !running[id] {
			delete(c.m, id)
			changed = true
		}
	}
	if changed {
		c.save()
	}
}

// save writes the file through a temporary one, so a stop halfway never leaves half a file.
func (c *cursorFile) save() {
	b, _ := json.Marshal(c.m)
	tmp := c.path + ".tmp"
	err := os.MkdirAll(filepath.Dir(c.path), 0o700)
	if err == nil {
		err = os.WriteFile(tmp, b, 0o600)
	}
	if err == nil {
		err = os.Rename(tmp, c.path)
	}
	if err != nil {
		c.log.Warn("can't save the log cursors", "path", c.path, "err", err)
	}
}
