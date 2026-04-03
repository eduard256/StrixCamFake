package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/flv"
	"github.com/AlexxIT/go2rtc/pkg/mp4"
	"github.com/AlexxIT/go2rtc/pkg/mpegts"
	"github.com/rs/zerolog/log"
)

// startHTTPServer starts the HTTP server handling snapshots, HLS, MP4, MJPEG, FLV,
// MPEG-TS, AAC, ONVIF, and camera-style HTTP video/snapshot URLs.
func startHTTPServer(cfg *Config, mainStream, subStream *Stream, snap *Snapshot) {
	addr := ":" + cfg.HTTPPort
	mux := http.NewServeMux()

	// --- go2rtc-style API endpoints ---
	mux.HandleFunc("/api/stream.mp4", func(w http.ResponseWriter, r *http.Request) {
		handleMP4Stream(w, r, mainStream, subStream)
	})
	mux.HandleFunc("/api/frame.mp4", func(w http.ResponseWriter, r *http.Request) {
		handleMP4Keyframe(w, r, mainStream, subStream)
	})
	mux.HandleFunc("/api/stream.m3u8", func(w http.ResponseWriter, r *http.Request) {
		handleHLSStream(w, r, mainStream, subStream)
	})
	mux.HandleFunc("/api/hls/playlist.m3u8", handleHLSPlaylist)
	mux.HandleFunc("/api/hls/segment.ts", handleHLSSegmentTS)
	mux.HandleFunc("/api/hls/init.mp4", handleHLSInit)
	mux.HandleFunc("/api/hls/segment.m4s", handleHLSSegmentMP4)
	mux.HandleFunc("/api/stream.mjpeg", func(w http.ResponseWriter, r *http.Request) {
		handleMJPEGStream(w, r, mainStream, subStream, cfg.RTSPPort)
	})
	mux.HandleFunc("/api/frame.jpeg", func(w http.ResponseWriter, r *http.Request) {
		handleJPEGSnapshot(w, r, snap)
	})
	mux.HandleFunc("/api/stream.flv", func(w http.ResponseWriter, r *http.Request) {
		handleFLVStream(w, r, mainStream, subStream)
	})
	mux.HandleFunc("/api/stream.ts", func(w http.ResponseWriter, r *http.Request) {
		handleMPEGTSStream(w, r, mainStream, subStream)
	})
	mux.HandleFunc("/api/stream.aac", func(w http.ResponseWriter, r *http.Request) {
		handleAACStream(w, r, mainStream, subStream)
	})

	// --- Camera-style HTTP snapshot URLs (from StrixCamDB data) ---
	mux.HandleFunc("/tmpfs/auto.jpg", func(w http.ResponseWriter, r *http.Request) {
		handleJPEGSnapshot(w, r, snap)
	})
	mux.HandleFunc("/cgi-bin/snapshot.cgi", func(w http.ResponseWriter, r *http.Request) {
		if !checkHTTPAuth(r, cfg.Username, cfg.Password) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		handleJPEGSnapshot(w, r, snap)
	})
	mux.HandleFunc("/img/snapshot.cgi", func(w http.ResponseWriter, r *http.Request) {
		handleJPEGSnapshot(w, r, snap)
	})
	mux.HandleFunc("/snapshot.jpg", func(w http.ResponseWriter, r *http.Request) {
		if !checkHTTPAuth(r, cfg.Username, cfg.Password) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		handleJPEGSnapshot(w, r, snap)
	})
	mux.HandleFunc("/snapshot.cgi", func(w http.ResponseWriter, r *http.Request) {
		if !checkHTTPAuth(r, cfg.Username, cfg.Password) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		handleJPEGSnapshot(w, r, snap)
	})

	// --- Camera-style HTTP video stream URLs ---
	mux.HandleFunc("/videostream.asf", func(w http.ResponseWriter, r *http.Request) {
		if !checkHTTPAuth(r, cfg.Username, cfg.Password) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		handleMJPEGStream(w, r, mainStream, subStream, cfg.RTSPPort)
	})
	mux.HandleFunc("/videostream.cgi", func(w http.ResponseWriter, r *http.Request) {
		if !checkHTTPAuth(r, cfg.Username, cfg.Password) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		handleMJPEGStream(w, r, mainStream, subStream, cfg.RTSPPort)
	})
	mux.HandleFunc("/videofeed", func(w http.ResponseWriter, r *http.Request) {
		handleMJPEGStream(w, r, mainStream, subStream, cfg.RTSPPort)
	})

	// --- HTTPS-style endpoints ---
	mux.HandleFunc("/axis-cgi/mjpg/video.cgi", func(w http.ResponseWriter, r *http.Request) {
		handleMJPEGStream(w, r, mainStream, subStream, cfg.RTSPPort)
	})
	mux.HandleFunc("/video/mjpg.cgi", func(w http.ResponseWriter, r *http.Request) {
		handleMJPEGStream(w, r, mainStream, subStream, cfg.RTSPPort)
	})
	mux.HandleFunc("/mjpeg/video.mjpg", func(w http.ResponseWriter, r *http.Request) {
		handleMJPEGStream(w, r, mainStream, subStream, cfg.RTSPPort)
	})

	// --- ONVIF ---
	mux.HandleFunc("/onvif/", func(w http.ResponseWriter, r *http.Request) {
		handleONVIF(w, r, cfg)
	})

	// --- Web UI (minimal login page) ---
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		handleWebUI(w, r, cfg)
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal().Err(err).Msg("[http] listen failed")
	}

	log.Info().Str("addr", addr).Msg("[http] listening")

	go func() {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("[http] serve")
		}
	}()
}

// resolveHTTPStream picks main or sub stream based on query parameters.
func resolveHTTPStream(r *http.Request, main, sub *Stream) *Stream {
	q := r.URL.Query()
	// support ?src=sub, ?stream=1, ?subtype=1, ?channel=sub
	switch {
	case q.Get("src") == "sub":
		return sub
	case q.Get("stream") == "1":
		return sub
	case q.Get("subtype") == "1":
		return sub
	case q.Get("channel") == "sub":
		return sub
	}
	return main
}

func handleMP4Stream(w http.ResponseWriter, r *http.Request, main, sub *Stream) {
	stream := resolveHTTPStream(r, main, sub)
	if !stream.HasProducer() {
		http.Error(w, "stream not ready", http.StatusServiceUnavailable)
		return
	}

	medias := mp4.ParseQuery(r.URL.Query())
	cons := mp4.NewConsumer(medias)
	cons.FormatName = "mp4"
	cons.WithRequest(r)

	if err := stream.AddConsumer(cons); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", mp4.ContentType(cons.Codecs()))

	ctx := r.Context()
	if i := core.Atoi(r.URL.Query().Get("duration")); i > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(i)*time.Second)
		defer cancel()
	}

	go func() {
		<-ctx.Done()
		_ = cons.Stop()
		stream.RemoveConsumer(cons)
	}()

	_, _ = cons.WriteTo(w)
}

func handleMP4Keyframe(w http.ResponseWriter, r *http.Request, main, sub *Stream) {
	stream := resolveHTTPStream(r, main, sub)
	if !stream.HasProducer() {
		http.Error(w, "stream not ready", http.StatusServiceUnavailable)
		return
	}

	cons := mp4.NewKeyframe(nil)
	if err := stream.AddConsumer(cons); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	once := &core.OnceBuffer{}
	_, _ = cons.WriteTo(once)
	stream.RemoveConsumer(cons)

	w.Header().Set("Content-Type", mp4.ContentType(cons.Codecs()))
	w.Header().Set("Content-Length", strconv.Itoa(once.Len()))
	_, _ = once.WriteTo(w)
}

// handleMJPEGStream transcodes H264 -> MJPEG via ffmpeg and outputs multipart JPEG stream.
func handleMJPEGStream(w http.ResponseWriter, r *http.Request, main, sub *Stream, rtspPort string) {
	stream := resolveHTTPStream(r, main, sub)
	if !stream.HasProducer() {
		http.Error(w, "stream not ready", http.StatusServiceUnavailable)
		return
	}

	// determine which internal RTSP stream to read
	streamName := "main"
	if stream.name == "sub" {
		streamName = "sub"
	}
	rtspURL := "rtsp://127.0.0.1:" + rtspPort + "/" + streamName

	cmd := exec.Command("ffmpeg",
		"-loglevel", "error",
		"-rtsp_transport", "tcp",
		"-i", rtspURL,
		"-f", "mpjpeg",
		"-q:v", "5",
		"-r", "10",
		"pipe:1",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := cmd.Start(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=ffmpeg")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "close")
	w.Header().Set("Pragma", "no-cache")

	// pipe ffmpeg mpjpeg output directly to HTTP response
	_, _ = io.Copy(w, stdout)

	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

func handleFLVStream(w http.ResponseWriter, r *http.Request, main, sub *Stream) {
	stream := resolveHTTPStream(r, main, sub)
	if !stream.HasProducer() {
		http.Error(w, "stream not ready", http.StatusServiceUnavailable)
		return
	}

	cons := flv.NewConsumer()
	cons.WithRequest(r)

	if err := stream.AddConsumer(cons); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "video/x-flv")
	_, _ = cons.WriteTo(w)

	stream.RemoveConsumer(cons)
}

func handleMPEGTSStream(w http.ResponseWriter, r *http.Request, main, sub *Stream) {
	stream := resolveHTTPStream(r, main, sub)
	if !stream.HasProducer() {
		http.Error(w, "stream not ready", http.StatusServiceUnavailable)
		return
	}

	cons := mpegts.NewConsumer()
	cons.WithRequest(r)

	if err := stream.AddConsumer(cons); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "video/mp2t")
	_, _ = cons.WriteTo(w)

	stream.RemoveConsumer(cons)
}

func handleAACStream(w http.ResponseWriter, r *http.Request, main, sub *Stream) {
	stream := resolveHTTPStream(r, main, sub)
	if !stream.HasProducer() {
		http.Error(w, "stream not ready", http.StatusServiceUnavailable)
		return
	}

	// AAC consumer from go2rtc pkg/aac
	cons := newAACConsumer()
	if cons == nil {
		http.Error(w, "aac not supported", http.StatusNotImplemented)
		return
	}

	if err := stream.AddConsumer(cons); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "audio/aac")
	_, _ = cons.(interface{ WriteTo(w interface{}) (int64, error) }).WriteTo(w)

	stream.RemoveConsumer(cons)
}

func handleJPEGSnapshot(w http.ResponseWriter, r *http.Request, snap *Snapshot) {
	data := snap.Get()
	if data == nil {
		http.Error(w, "no snapshot available", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

// checkHTTPAuth validates username/password from query params (camera-style).
func checkHTTPAuth(r *http.Request, username, password string) bool {
	if username == "" {
		return true
	}

	q := r.URL.Query()

	// try query params first (camera style: ?user=...&pwd=...)
	u := q.Get("user")
	if u == "" {
		u = q.Get("loginuse")
	}
	p := q.Get("pwd")
	if p == "" {
		p = q.Get("loginpas")
	}
	if p == "" {
		p = q.Get("password")
	}

	if u != "" || p != "" {
		return u == username && p == password
	}

	// try basic auth
	u, p, ok := r.BasicAuth()
	if ok {
		return u == username && p == password
	}

	// no credentials provided -- allow for some endpoints
	return true
}

// newAACConsumer tries to create an AAC consumer. Returns nil if not available.
func newAACConsumer() core.Consumer {
	// AAC consumer requires the aac package. We use a simple approach:
	// create a consumer that only accepts AAC audio.
	return nil // TODO: implement when aac.NewConsumer is available
}

func handleWebUI(w http.ResponseWriter, r *http.Request, cfg *Config) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
<head>
<title>` + cfg.CameraName + `</title>
<style>
body { font-family: Arial, sans-serif; background: #1a1a2e; color: #eee; display: flex;
  justify-content: center; align-items: center; height: 100vh; margin: 0; }
.container { text-align: center; background: #16213e; padding: 40px; border-radius: 8px;
  box-shadow: 0 4px 20px rgba(0,0,0,0.5); }
h1 { color: #0f3460; font-size: 18px; margin-bottom: 20px; }
.info { color: #999; font-size: 12px; margin-top: 20px; }
.info div { margin: 4px 0; }
</style>
</head>
<body>
<div class="container">
  <h1>` + cfg.CameraName + ` - ` + cfg.CameraModel + `</h1>
  <div class="info">
    <div>Firmware: ` + cfg.CameraFirmware + `</div>
    <div>Serial: ` + cfg.CameraSerial + `</div>
    <div>RTSP: rtsp://IP:` + cfg.RTSPPort + `/Streaming/Channels/101</div>
  </div>
</div>
</body>
</html>`))
}

// --- helper for camera-style URL params ---

func getResolutionStream(r *http.Request, main, sub *Stream) *Stream {
	q := r.URL.Query()
	// resolution=32 is typically sub stream, resolution=64 is main
	if q.Get("resolution") == "32" {
		return sub
	}
	if strings.Contains(q.Get("resolution"), "x") {
		// resolution=WIDTHxHEIGHT -- check if small
		parts := strings.Split(q.Get("resolution"), "x")
		if len(parts) == 2 {
			if w := core.Atoi(parts[0]); w > 0 && w <= 640 {
				return sub
			}
		}
	}
	return main
}
