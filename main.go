package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	// load .env file if present
	loadDotEnv(".env")

	log.Logger = zerolog.New(zerolog.ConsoleWriter{
		Out: os.Stdout, TimeFormat: "15:04:05",
	}).With().Timestamp().Logger()

	cfg := LoadConfig()

	// validate video files exist
	for _, f := range []string{cfg.MainVideo, cfg.SubVideo} {
		if _, err := os.Stat(f); err != nil {
			log.Fatal().Str("file", f).Msg("video file not found")
		}
	}

	log.Info().
		Str("main", cfg.MainVideo).
		Str("sub", cfg.SubVideo).
		Str("rtsp", ":"+cfg.RTSPPort).
		Str("http", ":"+cfg.HTTPPort).
		Str("rtmp", ":"+cfg.RTMPPort).
		Str("dvrip", ":"+cfg.DVRIPPort).
		Msg("[app] starting StrixCamFake")

	// create streams
	mainStream := NewStream("main")
	subStream := NewStream("sub")

	// create snapshot holder
	snap := NewSnapshot()

	// start all servers
	startRTSPServer(cfg.RTSPPort, cfg.Username, cfg.Password, mainStream, subStream)
	startHTTPServer(cfg, mainStream, subStream, snap)
	startRTMPServer(cfg.RTMPPort, mainStream, subStream)
	startBubbleServer(cfg.BubblePort, cfg.Username, cfg.Password, mainStream, subStream)
	startDVRIPServer(cfg.DVRIPPort, cfg.Username, cfg.Password, mainStream, subStream)
	// WS-Discovery is part of the ONVIF stack: if ONVIF is disabled we must
	// not advertise the camera on the multicast group at all, otherwise
	// clients would discover us and then fail on the (missing) SOAP endpoint.
	if cfg.ONVIFEnabled {
		startWSDiscovery(cfg.HTTPPort, cfg.CameraName)
	}

	// wait for servers to bind, then start ffmpeg pushers
	time.Sleep(500 * time.Millisecond)

	absMain, _ := filepath.Abs(cfg.MainVideo)
	absSub, _ := filepath.Abs(cfg.SubVideo)
	startFFmpeg(absMain, "main", cfg.RTSPPort)
	startFFmpeg(absSub, "sub", cfg.RTSPPort)

	// start snapshot loop
	StartSnapshotLoop(snap, cfg.RTSPPort, cfg.SnapshotInterval)

	printEndpoints(cfg)

	// block forever
	select {}
}

func printEndpoints(cfg *Config) {
	ip := getFirstIP()
	if ip == "" {
		ip = "127.0.0.1"
	}

	fmt.Println()
	fmt.Println("=== StrixCamFake ready ===")
	fmt.Println()
	fmt.Println("RTSP streams (main):")
	fmt.Printf("  rtsp://%s:%s/11\n", ip, cfg.RTSPPort)
	fmt.Printf("  rtsp://%s:%s/0\n", ip, cfg.RTSPPort)
	fmt.Printf("  rtsp://%s:%s/Streaming/Channels/101\n", ip, cfg.RTSPPort)
	fmt.Printf("  rtsp://%s:%s/cam/realmonitor?subtype=0\n", ip, cfg.RTSPPort)
	fmt.Printf("  rtsp://%s:%s/stream1\n", ip, cfg.RTSPPort)
	fmt.Printf("  rtsp://%s:%s/live/ch0\n", ip, cfg.RTSPPort)
	fmt.Printf("  rtsp://%s:%s/user=%s_password=%s_channel=1_stream=0.sdp\n",
		ip, cfg.RTSPPort, cfg.Username, cfg.Password)
	fmt.Println()
	fmt.Println("RTSP streams (sub):")
	fmt.Printf("  rtsp://%s:%s/12\n", ip, cfg.RTSPPort)
	fmt.Printf("  rtsp://%s:%s/1\n", ip, cfg.RTSPPort)
	fmt.Printf("  rtsp://%s:%s/Streaming/Channels/102\n", ip, cfg.RTSPPort)
	fmt.Printf("  rtsp://%s:%s/cam/realmonitor?subtype=1\n", ip, cfg.RTSPPort)
	fmt.Printf("  rtsp://%s:%s/stream2\n", ip, cfg.RTSPPort)
	fmt.Printf("  rtsp://%s:%s/live/ch1\n", ip, cfg.RTSPPort)
	fmt.Printf("  rtsp://%s:%s/user=%s_password=%s_channel=1_stream=1.sdp\n",
		ip, cfg.RTSPPort, cfg.Username, cfg.Password)
	fmt.Println()
	fmt.Println("HTTP endpoints:")
	fmt.Printf("  http://%s:%s/                           Web UI\n", ip, cfg.HTTPPort)
	fmt.Printf("  http://%s:%s/api/stream.mp4             MP4 stream\n", ip, cfg.HTTPPort)
	fmt.Printf("  http://%s:%s/api/frame.mp4              MP4 keyframe\n", ip, cfg.HTTPPort)
	fmt.Printf("  http://%s:%s/api/stream.m3u8            HLS\n", ip, cfg.HTTPPort)
	fmt.Printf("  http://%s:%s/api/stream.mjpeg           MJPEG stream\n", ip, cfg.HTTPPort)
	fmt.Printf("  http://%s:%s/api/frame.jpeg             JPEG snapshot\n", ip, cfg.HTTPPort)
	fmt.Printf("  http://%s:%s/api/stream.flv             FLV stream\n", ip, cfg.HTTPPort)
	fmt.Printf("  http://%s:%s/api/stream.ts              MPEG-TS stream\n", ip, cfg.HTTPPort)
	fmt.Printf("  http://%s:%s/api/stream.aac             AAC audio\n", ip, cfg.HTTPPort)
	fmt.Println()
	fmt.Println("Camera-style HTTP:")
	fmt.Printf("  http://%s:%s/tmpfs/auto.jpg             Snapshot\n", ip, cfg.HTTPPort)
	fmt.Printf("  http://%s:%s/snapshot.jpg               Snapshot (auth)\n", ip, cfg.HTTPPort)
	fmt.Printf("  http://%s:%s/videostream.cgi            MJPEG (auth)\n", ip, cfg.HTTPPort)
	fmt.Printf("  http://%s:%s/videofeed                  MJPEG\n", ip, cfg.HTTPPort)
	fmt.Println()
	fmt.Println("RTMP (main):")
	fmt.Printf("  rtmp://%s:%s/main\n", ip, cfg.RTMPPort)
	fmt.Printf("  rtmp://%s:%s/live\n", ip, cfg.RTMPPort)
	fmt.Printf("  rtmp://%s:%s/live/BalconyCam\n", ip, cfg.RTMPPort)
	fmt.Printf("  rtmp://%s:%s/floodlight-cam\n", ip, cfg.RTMPPort)
	fmt.Printf("  rtmp://%s:%s/bcs/channel0_main.bcs?channel=0&stream=0&user=%s&password=%s\n", ip, cfg.RTMPPort, cfg.Username, cfg.Password)
	fmt.Println()
	fmt.Println("RTMP (sub):")
	fmt.Printf("  rtmp://%s:%s/sub\n", ip, cfg.RTMPPort)
	fmt.Printf("  rtmp://%s:%s/bcs/channel0_sub.bcs?channel=0&stream=1&user=%s&password=%s\n", ip, cfg.RTMPPort, cfg.Username, cfg.Password)
	fmt.Printf("  rtmp://%s:%s/bcs/channel0_ext.bcs?channel=0&stream=2&user=%s&password=%s\n", ip, cfg.RTMPPort, cfg.Username, cfg.Password)
	fmt.Println()
	fmt.Println("DVRIP (main):")
	fmt.Printf("  dvrip://%s:%s@%s:%s?channel=0&subtype=0\n", cfg.Username, cfg.Password, ip, cfg.DVRIPPort)
	fmt.Println()
	fmt.Println("DVRIP (sub):")
	fmt.Printf("  dvrip://%s:%s@%s:%s?channel=0&subtype=1\n", cfg.Username, cfg.Password, ip, cfg.DVRIPPort)
	fmt.Println()
	if cfg.ONVIFEnabled {
		fmt.Println("ONVIF:")
		fmt.Printf("  http://%s:%s/onvif/device_service       ONVIF SOAP\n", ip, cfg.HTTPPort)
		fmt.Printf("  WS-Discovery on 239.255.255.250:3702\n")
	} else {
		fmt.Println("ONVIF: disabled (set ONVIF_ENABLED=true to enable)")
	}
	fmt.Println()
}

// loadDotEnv reads a .env file and sets environment variables.
// Minimal implementation -- no external dependency needed.
func loadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return // .env is optional
	}

	for _, line := range splitLines(string(data)) {
		line = trimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		key, val, ok := cut(line, "=")
		if !ok {
			continue
		}
		key = trimSpace(key)
		val = trimSpace(val)
		// strip quotes
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		// don't override existing env vars
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}

func splitLines(s string) []string {
	var lines []string
	for {
		i := indexByte(s, '\n')
		if i < 0 {
			if s != "" {
				lines = append(lines, s)
			}
			return lines
		}
		line := s[:i]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		lines = append(lines, line)
		s = s[i+1:]
	}
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

func cut(s, sep string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if i+len(sep) <= len(s) && s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}

// ensure http is imported
var _ http.Handler
