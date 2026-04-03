package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h264/annexb"
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

// handleBubble serves the bubble protocol over HTTP.
// Client connects to /bubble/live?ch=0&stream=0, gets upgraded to binary protocol.
func handleBubble(w http.ResponseWriter, r *http.Request, mainStream, subStream *Stream, username, password string) {
	q := r.URL.Query()
	streamIdx := q.Get("stream") // 0=main, 1=sub

	stream := mainStream
	if streamIdx == "1" {
		stream = subStream
	}

	if !stream.HasProducer() {
		http.Error(w, "stream not ready", http.StatusServiceUnavailable)
		return
	}

	// hijack the connection to get raw TCP access
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}

	conn, buf, err := hj.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer conn.Close()

	log.Debug().Str("remote", conn.RemoteAddr().String()).Msg("[bubble] new connection")

	// Step 1: send HTTP 200 response
	_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: video/bubble\r\n\r\n"))

	// Step 2: send XML device description (padded to 1024 bytes)
	xml := buildBubbleXML()
	_, _ = conn.Write(xml)

	// Step 3: read auth packet
	if err := bubbleReadAuth(buf.Reader, conn, username, password); err != nil {
		log.Debug().Err(err).Msg("[bubble] auth failed")
		return
	}

	// Step 4: read start packet
	if err := bubbleReadStart(buf.Reader); err != nil {
		log.Debug().Err(err).Msg("[bubble] start failed")
		return
	}

	log.Debug().Str("stream", stream.name).Msg("[bubble] streaming started")

	// Step 5: create a consumer that captures RTP packets and sends as bubble media
	bubbleStreamData(conn, stream)
}

func buildBubbleXML() []byte {
	xml := `<bubble version="1.0" vin="1"><vin0 stream="2">` +
		`<stream0 name="1080p.264" size="1920x1080" x1="yes" x2="yes" x4="yes" />` +
		`<stream1 name="360p.264" size="640x360" x1="yes" x2="yes" x4="yes" />` +
		`</vin0></bubble>`

	// pad to exactly 1024 bytes (as real cameras do)
	b := make([]byte, 1024)
	copy(b, xml)
	return b
}

func bubbleReadAuth(r *bufio.Reader, conn net.Conn, username, password string) error {
	cmd, payload, err := bubbleReadPacket(r)
	if err != nil {
		return err
	}

	if cmd != bubblePacketAuth || len(payload) < 48 {
		return fmt.Errorf("unexpected auth packet: cmd=%d len=%d", cmd, len(payload))
	}

	// payload: size(4) + unknown(4) + user(20) + pass(20)
	user := strings.TrimRight(string(payload[8:28]), "\x00")
	pass := strings.TrimRight(string(payload[28:48]), "\x00")

	if username != "" && (user != username || pass != password) {
		log.Debug().Str("user", user).Msg("[bubble] wrong credentials")
		// still send OK -- some cameras don't validate auth
	}

	// send auth OK response
	resp := make([]byte, 44)
	binary.BigEndian.PutUint32(resp, 40)
	resp[4] = 3 // auth OK flag
	resp[8] = 1 // success

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
	// 0xAA + size(uint32) + cmd(byte) + ts(uint32) + payload
	hdr := make([]byte, 10)
	if _, err := r.Read(hdr); err != nil {
		return 0, nil, err
	}

	if hdr[0] != bubbleSyncByte {
		return 0, nil, fmt.Errorf("wrong sync byte: %02x", hdr[0])
	}

	size := binary.BigEndian.Uint32(hdr[1:])
	payload := make([]byte, size-1-4)
	n := 0
	for n < len(payload) {
		nn, err := r.Read(payload[n:])
		if err != nil {
			return 0, nil, err
		}
		n += nn
	}

	return hdr[5], payload, nil
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

// bubbleStreamData connects to the stream as a consumer and sends H264 frames via bubble protocol.
func bubbleStreamData(conn net.Conn, stream *Stream) {
	// create a simple consumer that receives video packets
	cons := &bubbleConsumer{
		conn:   conn,
		stream: stream,
	}

	// set up media -- we want video H264
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

	// block until connection closes
	<-cons.done
}

type bubbleConsumer struct {
	conn    net.Conn
	stream  *Stream
	medias  []*core.Media
	sender  *core.Sender
	done    chan struct{}
}

func (c *bubbleConsumer) GetMedias() []*core.Media {
	return c.medias
}

func (c *bubbleConsumer) AddTrack(media *core.Media, codec *core.Codec, track *core.Receiver) error {
	c.done = make(chan struct{})

	sender := core.NewSender(media, codec)
	sender.Handler = func(pkt *rtp.Packet) {
		// convert AVCC to Annex B for bubble protocol
		data := annexb.DecodeAVCC(pkt.Payload, true)

		// bubble media packet: size(4) + type(1) + channel(1) + data
		// type: 1=keyframe, 2=other frame
		frameType := byte(2)
		if pkt.Marker {
			frameType = 1
		}

		payload := make([]byte, 6+len(data))
		binary.BigEndian.PutUint32(payload, uint32(len(data)))
		payload[4] = frameType
		payload[5] = 0 // channel

		copy(payload[6:], data)

		if err := bubbleWritePacket(c.conn, bubblePacketMedia, pkt.Timestamp/90, payload); err != nil {
			close(c.done)
		}
	}

	sender.HandleRTP(track)
	c.sender = sender

	return nil
}

func (c *bubbleConsumer) Stop() error {
	if c.sender != nil {
		c.sender.Close()
	}
	return c.conn.Close()
}
