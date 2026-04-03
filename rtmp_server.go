package main

import (
	"net"
	"strings"

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

// resolveRTMPStream maps RTMP app name to main or sub stream.
//
// Supported patterns:
//   - "" or "/" or "main" or "live" or "live/*" -> main (GoPro, OBS, Anpviz, TP-Link)
//   - "sub" or "live/sub" -> sub
//   - "floodlight-cam", "sideway-cam", any name -> main (Wyze bridge)
//   - "/bcs/channel0_main.bcs*" -> main (Reolink)
//   - "/bcs/channel0_sub.bcs*" -> sub (Reolink)
//   - "/bcs/channel0_ext.bcs*" -> sub (Reolink extended)
//   - "/bcs/channel1_main.bcs*" -> main (Reolink multi-channel)
//   - "?stream=1" or "?stream=2" in query -> sub
func resolveRTMPStream(app string, main, sub *Stream) *Stream {
	// normalize: remove leading slash
	app = strings.TrimPrefix(app, "/")

	// Reolink BCS style: bcs/channel0_sub.bcs, bcs/channel0_ext.bcs
	if strings.HasPrefix(app, "bcs/") {
		if strings.Contains(app, "_sub.") || strings.Contains(app, "_ext.") {
			return sub
		}
		// stream=1 or stream=2 in query = sub
		if strings.Contains(app, "stream=1") || strings.Contains(app, "stream=2") {
			return sub
		}
		return main
	}

	// explicit sub
	if app == "sub" || app == "live/sub" {
		return sub
	}

	// everything else is main:
	// "", "main", "live", "live/BalconyCam", "live/anything",
	// "floodlight-cam", "sideway-cam", etc.
	return main
}
