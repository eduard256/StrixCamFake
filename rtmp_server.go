package main

import (
	"net"

	"github.com/AlexxIT/go2rtc/pkg/flv"
	"github.com/AlexxIT/go2rtc/pkg/rtmp"
	"github.com/rs/zerolog/log"
)

// startRTMPServer starts the RTMP server.
// Clients connecting with "play" get video from the appropriate stream.
// Stream name "main" or default -> main stream, "sub" -> sub stream.
// Also supports Reolink-style paths: channel0_main.bcs, channel0_sub.bcs.
func startRTMPServer(port string, mainStream, subStream *Stream) {
	addr := ":" + port

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal().Err(err).Msg("[rtmp] listen failed")
	}

	log.Info().Str("addr", addr).Msg("[rtmp] listening")

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				log.Error().Err(err).Msg("[rtmp] accept")
				return
			}
			go handleRTMP(conn, mainStream, subStream)
		}
	}()
}

func handleRTMP(conn net.Conn, mainStream, subStream *Stream) {
	defer conn.Close()

	c, err := rtmp.NewServer(conn)
	if err != nil {
		log.Debug().Err(err).Msg("[rtmp] handshake")
		return
	}

	if err = c.ReadCommands(); err != nil {
		log.Debug().Err(err).Msg("[rtmp] commands")
		return
	}

	if c.Intent != rtmp.CommandPlay {
		log.Debug().Str("intent", c.Intent).Msg("[rtmp] unsupported intent")
		return
	}

	stream := resolveRTMPStream(c.App, mainStream, subStream)
	if !stream.HasProducer() {
		log.Debug().Str("app", c.App).Msg("[rtmp] stream not ready")
		return
	}

	cons := flv.NewConsumer()
	if err = stream.AddConsumer(cons); err != nil {
		log.Debug().Err(err).Msg("[rtmp] add consumer")
		return
	}

	defer stream.RemoveConsumer(cons)

	if err = c.WriteStart(); err != nil {
		log.Debug().Err(err).Msg("[rtmp] write start")
		return
	}

	log.Debug().Str("app", c.App).Msg("[rtmp] new consumer")

	_, _ = cons.WriteTo(c)
}

func resolveRTMPStream(app string, main, sub *Stream) *Stream {
	switch {
	case app == "sub":
		return sub
	case app == "live/sub":
		return sub
	// Reolink-style: channel0_sub.bcs
	case len(app) > 4 && app[len(app)-4:] == "_sub":
		return sub
	case contains(app, "sub"):
		return sub
	case contains(app, "stream=1"):
		return sub
	}
	return main
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
