package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/mp4"
	"github.com/AlexxIT/go2rtc/pkg/mpegts"
	"github.com/rs/zerolog/log"
)

const hlsKeepAlive = 5 * time.Second

var hlsSessions = map[string]*HLSSession{}
var hlsMu sync.RWMutex

// HLSSession holds state for one HLS client.
type HLSSession struct {
	id     string
	cons   core.Consumer
	stream *Stream
	alive  *time.Timer
	isFMP4 bool

	mu       sync.Mutex
	segments [][]byte
	seqNo    int
	ready    chan struct{}
}

func newHLSSessionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Run reads from consumer and splits into ~1-second segments.
func (s *HLSSession) Run() {
	// segmentWriter collects data written by the consumer into segments
	sw := &segmentWriter{session: s}
	cons := s.cons

	// WriteTo blocks until consumer stops
	if c, ok := cons.(interface {
		WriteTo(io.Writer) (int64, error)
	}); ok {
		_, _ = c.WriteTo(sw)
	}
}

func (s *HLSSession) addSegment(data []byte) {
	s.mu.Lock()
	s.segments = append(s.segments, data)
	if len(s.segments) > 5 {
		s.segments = s.segments[1:]
	}
	s.seqNo++

	// signal first segment ready
	select {
	case s.ready <- struct{}{}:
	default:
	}
	s.mu.Unlock()
}

func (s *HLSSession) Playlist() []byte {
	s.mu.Lock()
	seqNo := s.seqNo
	count := len(s.segments)
	s.mu.Unlock()

	if count == 0 {
		return nil
	}

	startSeq := seqNo - count

	var buf bytes.Buffer
	buf.WriteString("#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-TARGETDURATION:2\n")
	fmt.Fprintf(&buf, "#EXT-X-MEDIA-SEQUENCE:%d\n", startSeq)

	if s.isFMP4 {
		fmt.Fprintf(&buf, "#EXT-X-MAP:URI=\"init.mp4?id=%s\"\n", s.id)
	}

	for i := 0; i < count; i++ {
		buf.WriteString("#EXTINF:1.0,\n")
		if s.isFMP4 {
			fmt.Fprintf(&buf, "segment.m4s?id=%s&n=%d\n", s.id, startSeq+i)
		} else {
			fmt.Fprintf(&buf, "segment.ts?id=%s&n=%d\n", s.id, startSeq+i)
		}
	}

	return buf.Bytes()
}

func (s *HLSSession) Segment(n int) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	startSeq := s.seqNo - len(s.segments)
	idx := n - startSeq
	if idx < 0 || idx >= len(s.segments) {
		return nil
	}
	return s.segments[idx]
}

// segmentWriter splits incoming data into ~64KB chunks as segments.
// In practice the consumer writes whole frames/GOPs, so chunks approximate segments.
type segmentWriter struct {
	session *HLSSession
	buf     bytes.Buffer
}

func (w *segmentWriter) Write(p []byte) (n int, err error) {
	n, err = w.buf.Write(p)
	// flush segment every ~64KB
	if w.buf.Len() >= 64*1024 {
		data := make([]byte, w.buf.Len())
		copy(data, w.buf.Bytes())
		w.buf.Reset()
		w.session.addSegment(data)
	}
	return
}

func handleHLSStream(w http.ResponseWriter, r *http.Request, mainStream, subStream *Stream) {
	stream := resolveHTTPStream(r, mainStream, subStream)
	if !stream.HasProducer() {
		http.Error(w, "stream not ready", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")

	var cons core.Consumer
	var isFMP4 bool

	medias := mp4.ParseQuery(r.URL.Query())
	if medias != nil {
		c := mp4.NewConsumer(medias)
		c.FormatName = "hls/fmp4"
		c.WithRequest(r)
		cons = c
		isFMP4 = true
	} else {
		c := mpegts.NewConsumer()
		c.FormatName = "hls/mpegts"
		c.WithRequest(r)
		cons = c
	}

	if err := stream.AddConsumer(cons); err != nil {
		log.Error().Err(err).Msg("[hls] add consumer")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	id := newHLSSessionID()
	session := &HLSSession{
		id:     id,
		cons:   cons,
		stream: stream,
		isFMP4: isFMP4,
		ready:  make(chan struct{}, 1),
	}

	session.alive = time.AfterFunc(hlsKeepAlive, func() {
		hlsMu.Lock()
		delete(hlsSessions, id)
		hlsMu.Unlock()
		stream.RemoveConsumer(cons)
	})

	hlsMu.Lock()
	hlsSessions[id] = session
	hlsMu.Unlock()

	go session.Run()

	// master playlist
	body := "#EXTM3U\n#EXT-X-VERSION:6\n"
	body += "#EXT-X-STREAM-INF:BANDWIDTH=2000000\n"
	body += fmt.Sprintf("hls/playlist.m3u8?id=%s\n", id)

	_, _ = w.Write([]byte(body))
}

func handleHLSPlaylist(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")

	sid := r.URL.Query().Get("id")
	hlsMu.RLock()
	session := hlsSessions[sid]
	hlsMu.RUnlock()

	if session == nil {
		http.NotFound(w, r)
		return
	}

	session.alive.Reset(hlsKeepAlive)

	data := session.Playlist()
	if data == nil {
		select {
		case <-session.ready:
		case <-time.After(3 * time.Second):
		}
		data = session.Playlist()
	}

	if data == nil {
		http.NotFound(w, r)
		return
	}

	_, _ = w.Write(data)
}

func handleHLSSegmentTS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "video/mp2t")

	q := r.URL.Query()
	sid := q.Get("id")

	hlsMu.RLock()
	session := hlsSessions[sid]
	hlsMu.RUnlock()

	if session == nil {
		http.NotFound(w, r)
		return
	}

	session.alive.Reset(hlsKeepAlive)

	n := core.Atoi(q.Get("n"))
	data := session.Segment(n)
	if data == nil {
		http.NotFound(w, r)
		return
	}

	_, _ = w.Write(data)
}

func handleHLSInit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "video/mp4")

	sid := r.URL.Query().Get("id")

	hlsMu.RLock()
	session := hlsSessions[sid]
	hlsMu.RUnlock()

	if session == nil || !session.isFMP4 {
		http.NotFound(w, r)
		return
	}

	// first segment of fMP4 contains the init segment
	data := session.Segment(session.seqNo - len(session.segments))
	if data == nil {
		http.NotFound(w, r)
		return
	}

	_, _ = w.Write(data)
}

func handleHLSSegmentMP4(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "video/iso.segment")

	q := r.URL.Query()
	sid := q.Get("id")

	hlsMu.RLock()
	session := hlsSessions[sid]
	hlsMu.RUnlock()

	if session == nil {
		http.NotFound(w, r)
		return
	}

	session.alive.Reset(hlsKeepAlive)

	n := core.Atoi(q.Get("n"))
	data := session.Segment(n)
	if data == nil {
		http.NotFound(w, r)
		return
	}

	_, _ = w.Write(data)
}
