package main

import (
	"errors"
	"io"
	"net"
	"net/url"
	"strings"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/rtsp"
	"github.com/AlexxIT/go2rtc/pkg/tcp"
	"github.com/rs/zerolog/log"
)

// startRTSPServer starts the RTSP server on the given port.
// It handles all URL patterns and routes them to main or sub stream.
func startRTSPServer(port, username, password string, mainStream, subStream *Stream) {
	addr := ":" + port

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal().Err(err).Msg("[rtsp] listen failed")
	}

	log.Info().Str("addr", addr).Msg("[rtsp] listening")

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				log.Error().Err(err).Msg("[rtsp] accept")
				return
			}
			go handleRTSP(conn, username, password, mainStream, subStream)
		}
	}()
}

func handleRTSP(conn net.Conn, username, password string, mainStream, subStream *Stream) {
	c := rtsp.NewServer(conn)

	if username != "" {
		c.Auth(username, password)
	}

	c.Listen(func(msg any) {
		switch msg {
		case rtsp.MethodDescribe:
			if c.URL == nil || len(c.URL.Path) == 0 {
				return
			}

			stream := resolveRTSPStream(c.URL, username, password, mainStream, subStream)
			if stream == nil || !stream.HasProducer() {
				return
			}

			c.SessionName = "StrixCamFake"

			// request video + audio
			c.Medias = []*core.Media{
				{
					Kind:      core.KindVideo,
					Direction: core.DirectionSendonly,
					Codecs: []*core.Codec{
						{Name: core.CodecH264},
						{Name: core.CodecH265},
					},
				},
				{
					Kind:      core.KindAudio,
					Direction: core.DirectionSendonly,
					Codecs: []*core.Codec{
						{Name: core.CodecAAC},
						{Name: core.CodecPCMA},
						{Name: core.CodecPCMU},
						{Name: core.CodecOpus},
					},
				},
			}

			if err := stream.AddConsumer(c); err != nil {
				log.Warn().Err(err).Msg("[rtsp] add consumer")
				return
			}

			log.Debug().Str("path", c.URL.Path).Msg("[rtsp] new consumer")

		case rtsp.MethodAnnounce:
			// ffmpeg pushes stream via ANNOUNCE
			if c.URL == nil || len(c.URL.Path) == 0 {
				return
			}

			name := strings.TrimPrefix(c.URL.Path, "/")
			var stream *Stream
			switch name {
			case "main":
				stream = mainStream
			case "sub":
				stream = subStream
			default:
				return
			}

			stream.SetProducer(c)
			log.Info().Str("stream", name).Msg("[rtsp] producer connected")
		}
	})

	if err := c.Accept(); err != nil {
		if errors.Is(err, rtsp.FailedAuth) {
			log.Warn().Str("addr", conn.RemoteAddr().String()).Msg("[rtsp] auth failed")
		} else if err != io.EOF {
			log.Trace().Err(err).Msg("[rtsp] accept")
		}
		_ = conn.Close()
		return
	}

	if err := c.Handle(); err != nil {
		log.Debug().Err(err).Msg("[rtsp] handle")
	}

	_ = conn.Close()
}

// resolveRTSPStream maps an RTSP URL path to main or sub stream.
//
// Supported patterns:
//
//   XM/Generic:        /11 (main), /12 (sub)
//   Numeric:           /0 (main), /1 (sub)
//   XM SDP:            /user=..._channel=1_stream=0.sdp (main), stream=1.sdp (sub)
//   Hikvision:         /Streaming/Channels/101 (main), /102 (sub)
//   Dahua:             /cam/realmonitor?subtype=0 (main), subtype=1 (sub)
//   Generic stream:    /stream1 (main), /stream2 (sub)
//   Generic live:      /live/ch0 (main), /live/ch1 (sub)
//   Internal:          /main, /sub (used by ffmpeg ANNOUNCE)
func resolveRTSPStream(u *url.URL, username, password string, main, sub *Stream) *Stream {
	path := u.Path
	query := u.RawQuery

	switch {
	// Internal paths (ffmpeg ANNOUNCE + direct access)
	case path == "/main":
		return main
	case path == "/sub":
		return sub

	// XM/Generic: /11 = main, /12 = sub
	case path == "/11":
		return main
	case path == "/12":
		return sub

	// Numeric: /0 = main, /1 = sub
	case path == "/0":
		return main
	case path == "/1":
		return sub

	// Generic stream: /stream1 = main, /stream2 = sub
	case path == "/stream1":
		return main
	case path == "/stream2":
		return sub

	// Generic live: /live/ch0 = main, /live/ch1 = sub
	case path == "/live/ch0":
		return main
	case path == "/live/ch1":
		return sub

	// Hikvision: /Streaming/Channels/101 = main, /102 = sub
	case strings.HasPrefix(path, "/Streaming/Channels/"):
		ch := strings.TrimPrefix(path, "/Streaming/Channels/")
		return hikvisionChannel(ch, main, sub)

	// Dahua: /cam/realmonitor?subtype=0 = main, subtype=1 = sub
	case strings.HasPrefix(path, "/cam/realmonitor"):
		return dahuaSubtype(query, main, sub)

	// XM SDP: stream=0 = main, stream=1 = sub
	case strings.HasSuffix(path, ".sdp"):
		return xmSDP(path, username, password, main, sub)
	}

	// default to main for unknown paths
	return main
}

func hikvisionChannel(ch string, main, sub *Stream) *Stream {
	// 101 = channel 1 stream 1 (main), 102 = channel 1 stream 2 (sub)
	// 201 = channel 2 stream 1 (main), etc.
	if len(ch) >= 1 {
		last := ch[len(ch)-1]
		if last == '2' {
			return sub
		}
	}
	return main
}

func dahuaSubtype(query string, main, sub *Stream) *Stream {
	params, _ := url.ParseQuery(query)
	if params.Get("subtype") == "1" {
		return sub
	}
	return main
}

// xmSDP parses XM-style SDP URLs:
// /user=admin_password=admin_channel=1_stream=0.sdp
func xmSDP(path, username, password string, main, sub *Stream) *Stream {
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSuffix(path, ".sdp")

	// validate credentials if present in path
	params := map[string]string{}
	for _, part := range strings.Split(path, "_") {
		if k, v, ok := strings.Cut(part, "="); ok {
			params[k] = v
		}
	}

	if u, ok := params["user"]; ok {
		if u != username {
			return nil
		}
	}
	if p, ok := params["password"]; ok {
		if p != password {
			return nil
		}
	}

	if params["stream"] == "1" {
		return sub
	}
	return main
}

// parseRTSPAuth extracts credentials from tcp.Request for XM SDP pattern validation.
func parseRTSPAuth(req *tcp.Request) (string, string) {
	if req == nil {
		return "", ""
	}
	if info := req.URL.User; info != nil {
		pass, _ := info.Password()
		return info.Username(), pass
	}
	return "", ""
}
