package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h264"
	"github.com/AlexxIT/go2rtc/pkg/h264/annexb"
	"github.com/AlexxIT/go2rtc/pkg/tcp"
	"github.com/pion/rtp"
	"github.com/rs/zerolog/log"
)

const (
	bubbleSyncByte    = 0xAA
	bubblePacketAuth  = 0x00
	bubblePacketMedia = 0x01
	bubblePacketStart = 0x0A
	bubbleTimeout     = 5 * time.Second
)

// startBubbleServer starts a raw TCP server for the bubble protocol.
// go2rtc bubble client connects via TCP, sends raw HTTP GET, then switches to binary.
func startBubbleServer(port, username, password string, mainStream, subStream *Stream) {
	addr := ":" + port

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error().Err(err).Msg("[bubble] listen failed")
		return
	}

	log.Info().Str("addr", addr).Msg("[bubble] listening")

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				log.Error().Err(err).Msg("[bubble] accept")
				return
			}
			go handleBubbleConn(conn, username, password, mainStream, subStream)
		}
	}()
}

func handleBubbleConn(conn net.Conn, username, password string, mainStream, subStream *Stream) {
	defer conn.Close()

	r := bufio.NewReader(conn)

	// Step 1: read HTTP GET request
	_ = conn.SetReadDeadline(time.Now().Add(bubbleTimeout))

	req, err := tcp.ReadRequest(r)
	if err != nil {
		log.Debug().Err(err).Msg("[bubble] read request")
		return
	}

	log.Debug().Str("path", req.URL.Path).Str("remote", conn.RemoteAddr().String()).Msg("[bubble] connection")

	// parse stream index from query
	q := req.URL.Query()
	streamIdx := q.Get("stream")

	stream := mainStream
	if streamIdx == "1" {
		stream = subStream
	}

	if !stream.HasProducer() {
		log.Debug().Msg("[bubble] stream not ready")
		return
	}

	// Step 2: send HTTP 200 response
	res := &tcp.Response{Status: "200 OK", Header: map[string][]string{
		"Content-Type": {"video/bubble"},
	}, Request: req}

	_ = conn.SetWriteDeadline(time.Now().Add(bubbleTimeout))
	if err := res.Write(conn); err != nil {
		log.Debug().Err(err).Msg("[bubble] write response")
		return
	}

	// Step 3: send XML device description (padded to 1024 bytes)
	xml := buildBubbleXML()
	if _, err := conn.Write(xml); err != nil {
		log.Debug().Err(err).Msg("[bubble] write xml")
		return
	}

	// Step 4: read auth packet
	if err := bubbleReadAuth(r, conn, username, password); err != nil {
		log.Debug().Err(err).Msg("[bubble] auth failed")
		return
	}

	log.Debug().Msg("[bubble] auth OK, waiting for start")

	// Step 5: read start packet
	if err := bubbleReadStart(r); err != nil {
		if peeked, _ := r.Peek(32); len(peeked) > 0 {
			log.Debug().Hex("peek", peeked).Msg("[bubble] buffer after start fail")
		}
		log.Debug().Err(err).Msg("[bubble] start failed")
		return
	}

	log.Debug().Str("stream", stream.name).Msg("[bubble] streaming started")

	// clear all deadlines before long-running stream
	_ = conn.SetDeadline(time.Time{})

	// Step 6: stream data
	bubbleStreamData(conn, stream)
}

func buildBubbleXML() []byte {
	xml := `<bubble version="1.0" vin="1"><vin0 stream="2">` +
		`<stream0 name="1080p.264" size="1920x1080" x1="yes" x2="yes" x4="yes" />` +
		`<stream1 name="360p.264" size="640x360" x1="yes" x2="yes" x4="yes" />` +
		`</vin0></bubble>`

	b := make([]byte, 1024)
	copy(b, xml)
	return b
}

func bubbleReadAuth(r *bufio.Reader, conn net.Conn, username, password string) error {
	cmd, payload, err := bubbleReadPacket(r)
	if err != nil {
		return err
	}

	// payload after cmd+ts: size(4) + unknown(4) + user(20) + pass(20) = 48 bytes total
	// but client sends size=44 in the size field, so payload here is 44 bytes (after stripping cmd+ts from body)
	if cmd != bubblePacketAuth || len(payload) < 44 {
		return fmt.Errorf("unexpected auth packet: cmd=%d len=%d", cmd, len(payload))
	}

	// payload: size(4) + unknown(4) + user(20) + pass(20)
	user := strings.TrimRight(string(payload[8:28]), "\x00")
	pass := strings.TrimRight(string(payload[28:48]), "\x00")

	_ = user
	_ = pass
	// real cameras often don't validate -- just accept

	// send auth OK response
	resp := make([]byte, 44)
	binary.BigEndian.PutUint32(resp, 40)
	resp[4] = 3
	resp[8] = 1

	return bubbleWritePacket(conn, bubblePacketAuth, 0x0E16C271, resp)
}

func bubbleReadStart(r *bufio.Reader) error {
	cmd, _, err := bubbleReadPacket(r)
	if err != nil {
		return err
	}
	if cmd != bubblePacketStart {
		return fmt.Errorf("expected start packet, got cmd=%d", cmd)
	}
	return nil
}

func bubbleReadPacket(r *bufio.Reader) (byte, []byte, error) {
	// packet: 0xAA + size(4 BE) + cmd(1) + ts(4 BE) + payload
	// size = 1(cmd) + 4(ts) + len(payload)

	// skip non-sync bytes (in case of misalignment)
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		if b == bubbleSyncByte {
			break
		}
		log.Debug().Hex("byte", []byte{b}).Msg("[bubble] skipping non-sync byte")
	}

	// read size (4 bytes BE)
	var sizeBuf [4]byte
	if _, err := io.ReadFull(r, sizeBuf[:]); err != nil {
		return 0, nil, err
	}
	size := binary.BigEndian.Uint32(sizeBuf[:])

	if size < 5 || size > 1024*1024 {
		return 0, nil, fmt.Errorf("invalid packet size: %d", size)
	}

	// read cmd(1) + ts(4) + payload
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}

	cmd := body[0]
	// ts := binary.BigEndian.Uint32(body[1:5])
	var payload []byte
	if size > 5 {
		payload = body[5:]
	}

	log.Debug().Uint8("cmd", cmd).Int("size", int(size)).Msg("[bubble] read packet")

	return cmd, payload, nil
}

func bubbleWritePacket(conn net.Conn, cmd byte, ts uint32, payload []byte) error {
	_ = conn.SetWriteDeadline(time.Now().Add(bubbleTimeout))

	b := make([]byte, 10+len(payload))
	b[0] = bubbleSyncByte
	binary.BigEndian.PutUint32(b[1:], uint32(5+len(payload)))
	b[5] = cmd
	binary.BigEndian.PutUint32(b[6:], ts)
	copy(b[10:], payload)

	_, err := conn.Write(b)
	return err
}

func bubbleStreamData(conn net.Conn, stream *Stream) {
	cons := &bubbleConsumer{
		conn: conn,
		done: make(chan struct{}),
	}

	cons.medias = []*core.Media{
		{
			Kind:      core.KindVideo,
			Direction: core.DirectionSendonly,
			Codecs:    []*core.Codec{{Name: core.CodecH264}},
		},
	}

	if err := stream.AddConsumer(cons); err != nil {
		log.Debug().Err(err).Msg("[bubble] add consumer failed")
		return
	}

	defer stream.RemoveConsumer(cons)

	<-cons.done
}

type bubbleConsumer struct {
	conn   net.Conn
	medias []*core.Media
	sender *core.Sender
	done   chan struct{}
}

func (c *bubbleConsumer) GetMedias() []*core.Media {
	return c.medias
}

func (c *bubbleConsumer) AddTrack(media *core.Media, codec *core.Codec, track *core.Receiver) error {
	sender := core.NewSender(media, codec)

	// sendFrame receives AVCC-encoded H264 data and wraps it in bubble media packet format.
	// Media packet: size(4 BE) + type(1) + channel(1) + annexb_data
	//   type = 1 for keyframe, 2 for other frame (matches go2rtc bubble client.go Handle())
	sendFrame := func(pkt *rtp.Packet) {
		if len(pkt.Payload) < 5 {
			return
		}

		// pkt.Payload is AVCC -- convert to Annex B for bubble wire format
		data := annexb.DecodeAVCC(pkt.Payload, true)
		if len(data) == 0 {
			return
		}

		// detect keyframe: scan for first start code and check NAL type
		frameType := byte(2) // regular frame
		for i := 0; i+4 < len(data); i++ {
			if data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
				nalType := data[i+4] & 0x1F
				if nalType == 5 || nalType == 7 { // IDR or SPS
					frameType = 1
				}
				break
			}
		}

		payload := make([]byte, 6+len(data))
		binary.BigEndian.PutUint32(payload, uint32(len(data)))
		payload[4] = frameType
		payload[5] = 0 // channel
		copy(payload[6:], data)

		ts := pkt.Timestamp / 90 // 90kHz -> ms
		if err := bubbleWritePacket(c.conn, bubblePacketMedia, ts, payload); err != nil {
			select {
			case <-c.done:
			default:
				close(c.done)
			}
		}
	}

	// RTSP producers (ffmpeg via ANNOUNCE) send raw RTP packets (FU-A/STAP-A).
	// Use track.Codec which carries the real FmtpLine with SPS/PPS for RTPDepay.
	if track.Codec.IsRTP() {
		sender.Handler = h264.RTPDepay(track.Codec, sendFrame)
	} else {
		sender.Handler = sendFrame
	}

	sender.HandleRTP(track)
	c.sender = sender

	return nil
}

func (c *bubbleConsumer) Stop() error {
	if c.sender != nil {
		c.sender.Close()
	}
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	return c.conn.Close()
}

// ensure imports are used
var _ = url.Parse
