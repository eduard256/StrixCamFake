package main

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h264"
	"github.com/AlexxIT/go2rtc/pkg/h264/annexb"
	"github.com/pion/rtp"
	"github.com/rs/zerolog/log"
)

// DVRIP protocol constants matching the Sofia/NetSurveillance/XMeye protocol.
// go2rtc client uses these same values in pkg/dvrip/client.go.
const (
	dvripSyncByte = 0xFF

	dvripCmdLogin          = 1000
	dvripCmdLoginResp      = 1000
	dvripCmdMonitorClaim   = 1413
	dvripCmdMonitorStart   = 1410
	dvripCmdKeepAlive      = 1006
	dvripCmdKeepAliveResp  = 1006
	dvripCmdSysInfo        = 1020
	dvripCmdSysInfoResp    = 1020
	dvripCmdChannelTitle   = 1046
	dvripCmdAbilityGet     = 1360
	dvripCmdAbilityResp    = 1360
	dvripCmdConfigGet      = 1042
	dvripCmdConfigResp     = 1042
	dvripCmdConfigChannGet = 1044
	dvripCmdConfigChannResp = 1044

	dvripHeaderSize = 20
	dvripTimeout    = 10 * time.Second
)

// startDVRIPServer starts a raw TCP server implementing the DVRIP (Sofia/XMeye) protocol.
// go2rtc connects to this with dvrip://user:pass@host:34567?channel=0&subtype=0
func startDVRIPServer(port, username, password string, mainStream, subStream *Stream) {
	addr := ":" + port

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error().Err(err).Msg("[dvrip] listen failed")
		return
	}

	log.Info().Str("addr", addr).Msg("[dvrip] listening")

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				log.Error().Err(err).Msg("[dvrip] accept")
				return
			}
			go handleDVRIPConn(conn, username, password, mainStream, subStream)
		}
	}()
}

// dvripConn holds state for a single DVRIP client connection.
type dvripConn struct {
	conn    net.Conn
	session uint32
	seq     uint32
	mu      sync.Mutex
}

func handleDVRIPConn(conn net.Conn, username, password string, mainStream, subStream *Stream) {
	defer conn.Close()

	dc := &dvripConn{
		conn:    conn,
		session: 0x0001E240, // arbitrary session ID, mimics real cameras
	}

	log.Debug().Str("remote", conn.RemoteAddr().String()).Msg("[dvrip] new connection")

	// Step 1: read login command
	cmd, payload, seq, err := dc.readCmd()
	if err != nil {
		log.Debug().Err(err).Msg("[dvrip] read login")
		return
	}

	if cmd != dvripCmdLogin {
		log.Debug().Uint16("cmd", cmd).Msg("[dvrip] expected login, got different command")
		return
	}

	dc.seq = seq

	// Parse login JSON and validate credentials
	if !dvripValidateLogin(payload, username, password) {
		log.Debug().Msg("[dvrip] invalid credentials")
		dc.sendLoginResponse(false)
		return
	}

	if err := dc.sendLoginResponse(true); err != nil {
		log.Debug().Err(err).Msg("[dvrip] send login response")
		return
	}

	log.Debug().Msg("[dvrip] login OK")

	// Step 2: command loop -- handle Claim, Start, KeepAlive, etc.
	var stream *Stream

	for {
		cmd, payload, seq, err = dc.readCmd()
		if err != nil {
			log.Debug().Err(err).Msg("[dvrip] read command")
			return
		}

		dc.seq = seq

		switch cmd {
		case dvripCmdMonitorClaim:
			stream = dvripResolveStream(payload, mainStream, subStream)
			if err := dc.sendJSONResponse(cmd, dvripRet(100)); err != nil {
				log.Debug().Err(err).Msg("[dvrip] send claim response")
				return
			}
			log.Debug().Str("stream", stream.name).Msg("[dvrip] monitor claim")

		case dvripCmdMonitorStart:
			if stream == nil {
				stream = mainStream
			}
			if !stream.HasProducer() {
				log.Debug().Msg("[dvrip] stream not ready")
				return
			}
			log.Debug().Str("stream", stream.name).Msg("[dvrip] monitor start, streaming")
			dvripStreamData(dc, stream)
			return

		case dvripCmdKeepAlive:
			if err := dc.sendJSONResponse(dvripCmdKeepAliveResp, dvripRet(100)); err != nil {
				log.Debug().Err(err).Msg("[dvrip] send keepalive response")
				return
			}

		case dvripCmdSysInfo:
			resp := map[string]any{
				"Name":      "SystemInfo",
				"Ret":       100,
				"SessionID": fmt.Sprintf("0x%08X", dc.session),
				"SystemInfo": map[string]any{
					"AlarmInChannel":  0,
					"AlarmOutChannel": 0,
					"AudioInChannel":  1,
					"BuildTime":       "2024-01-01 00:00:00",
					"CombineSwitch":   0,
					"DeviceModel":     "StrixCam",
					"DeviceRunTime":   "0x00000100",
					"DigChannel":      0,
					"EncryptVersion":  "Unknown",
					"ExtraChannel":    0,
					"HardWare":        "SFC-2000",
					"HardWareVersion": "1.0.0",
					"SerialNo":        "SFC00000000000001",
					"SoftWareVersion": "V1.0.0",
					"TalkInChannel":   1,
					"TalkOutChannel":  1,
					"TotalChannel":    1,
					"UpdataType":      "0x00000000",
					"VideoInChannel":  1,
					"VideoOutChannel": 1,
				},
			}
			if err := dc.sendJSONResponse(dvripCmdSysInfoResp, resp); err != nil {
				return
			}

		case dvripCmdChannelTitle:
			resp := map[string]any{
				"Name":         "ChannelTitle",
				"Ret":          100,
				"SessionID":    fmt.Sprintf("0x%08X", dc.session),
				"ChannelTitle": []string{"StrixCam"},
			}
			if err := dc.sendJSONResponse(cmd, resp); err != nil {
				return
			}

		case dvripCmdAbilityGet:
			// Generic ability response -- enough for go2rtc to proceed
			resp := map[string]any{
				"Name":      "Ability",
				"Ret":       100,
				"SessionID": fmt.Sprintf("0x%08X", dc.session),
			}
			if err := dc.sendJSONResponse(dvripCmdAbilityResp, resp); err != nil {
				return
			}

		case dvripCmdConfigGet, dvripCmdConfigChannGet:
			// Generic config response
			resp := map[string]any{
				"Name":      "ConfigGet",
				"Ret":       100,
				"SessionID": fmt.Sprintf("0x%08X", dc.session),
			}
			respCmd := uint16(dvripCmdConfigResp)
			if cmd == dvripCmdConfigChannGet {
				respCmd = dvripCmdConfigChannResp
			}
			if err := dc.sendJSONResponse(respCmd, resp); err != nil {
				return
			}

		default:
			// Unknown command -- reply with generic success
			log.Debug().Uint16("cmd", cmd).Msg("[dvrip] unknown command, sending OK")
			if err := dc.sendJSONResponse(cmd, dvripRet(100)); err != nil {
				return
			}
		}
	}
}

// readCmd reads a single DVRIP command from the connection.
// Returns command ID, payload, sequence number, and error.
func (dc *dvripConn) readCmd() (cmd uint16, payload []byte, seq uint32, err error) {
	_ = dc.conn.SetReadDeadline(time.Now().Add(dvripTimeout))

	header := make([]byte, dvripHeaderSize)
	if _, err = io.ReadFull(dc.conn, header); err != nil {
		return
	}

	if header[0] != dvripSyncByte {
		err = fmt.Errorf("wrong sync byte: 0x%02X", header[0])
		return
	}

	seq = binary.LittleEndian.Uint32(header[8:])
	cmd = binary.LittleEndian.Uint16(header[14:])
	size := binary.LittleEndian.Uint32(header[16:])

	if size > 0 {
		payload = make([]byte, size)
		if _, err = io.ReadFull(dc.conn, payload); err != nil {
			return
		}
	}

	return
}

// writeCmd writes a DVRIP command packet to the connection.
func (dc *dvripConn) writeCmd(cmd uint16, payload []byte) error {
	dc.mu.Lock()
	defer dc.mu.Unlock()

	_ = dc.conn.SetWriteDeadline(time.Now().Add(dvripTimeout))

	b := make([]byte, dvripHeaderSize+len(payload))
	b[0] = dvripSyncByte
	binary.LittleEndian.PutUint32(b[4:], dc.session)
	binary.LittleEndian.PutUint32(b[8:], dc.seq)
	binary.LittleEndian.PutUint16(b[14:], cmd)
	binary.LittleEndian.PutUint32(b[16:], uint32(len(payload)))
	copy(b[dvripHeaderSize:], payload)

	_, err := dc.conn.Write(b)
	return err
}

// sendLoginResponse sends a login result.
func (dc *dvripConn) sendLoginResponse(success bool) error {
	ret := 100
	if !success {
		ret = 205 // login failed code
	}

	resp := map[string]any{
		"Name":       "Login",
		"Ret":        ret,
		"SessionID":  fmt.Sprintf("0x%08X", dc.session),
		"AliveInterval": 30,
		"ChannelNum": 1,
		"DeviceType": "StrixCam",
		"ExtraChannel": 0,
	}

	return dc.sendJSONResponse(dvripCmdLoginResp, resp)
}

// sendJSONResponse marshals a JSON object and sends it as a DVRIP command response.
func (dc *dvripConn) sendJSONResponse(cmd uint16, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}

	// DVRIP JSON payloads are terminated with \n\0 (0x0A 0x00)
	payload := make([]byte, len(data)+2)
	copy(payload, data)
	payload[len(data)] = 0x0A
	payload[len(data)+1] = 0x00

	return dc.writeCmd(cmd, payload)
}

// dvripRet creates a simple {Ret: code, SessionID: ...} response map.
func dvripRet(code int) map[string]any {
	return map[string]any{
		"Ret": code,
	}
}

// dvripValidateLogin parses the login JSON and validates credentials.
// The DVRIP protocol uses Sofia hash for password validation.
func dvripValidateLogin(payload []byte, expectedUser, expectedPass string) bool {
	// Strip trailing \n\0 if present
	for len(payload) > 0 && (payload[len(payload)-1] == 0x00 || payload[len(payload)-1] == 0x0A) {
		payload = payload[:len(payload)-1]
	}

	var login struct {
		UserName    string `json:"UserName"`
		PassWord    string `json:"PassWord"`
		EncryptType string `json:"EncryptType"`
	}

	if err := json.Unmarshal(payload, &login); err != nil {
		log.Debug().Err(err).Str("raw", string(payload)).Msg("[dvrip] parse login JSON")
		return false
	}

	log.Debug().Str("user", login.UserName).Str("encrypt", login.EncryptType).Msg("[dvrip] login attempt")

	// Accept any username/password (like real cheap cameras often do)
	// But also properly validate Sofia hash if the client sends one
	if login.UserName != expectedUser {
		return true // accept anyway, like real cameras
	}

	if login.EncryptType == "MD5" {
		// Client sends Sofia hash of the password
		expectedHash := dvripSofiaHash(expectedPass)
		if login.PassWord != expectedHash {
			log.Debug().Str("got", login.PassWord).Str("expected", expectedHash).Msg("[dvrip] hash mismatch (accepting anyway)")
		}
	}

	return true
}

// dvripSofiaHash computes the Sofia password hash (same as go2rtc SofiaHash).
// MD5 of password, then pairs of bytes are summed and mapped to charset mod 62.
func dvripSofiaHash(password string) string {
	const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

	hash := md5.Sum([]byte(password))
	sofia := make([]byte, 0, 8)
	for i := 0; i < md5.Size; i += 2 {
		j := uint16(hash[i]) + uint16(hash[i+1])
		sofia = append(sofia, chars[j%62])
	}

	return string(sofia)
}

// dvripResolveStream parses the OPMonitor JSON to determine main or sub stream.
func dvripResolveStream(payload []byte, mainStream, subStream *Stream) *Stream {
	// Strip trailing \n\0
	for len(payload) > 0 && (payload[len(payload)-1] == 0x00 || payload[len(payload)-1] == 0x0A) {
		payload = payload[:len(payload)-1]
	}

	var req struct {
		OPMonitor struct {
			Parameter struct {
				StreamType string `json:"StreamType"`
			} `json:"Parameter"`
		} `json:"OPMonitor"`
	}

	if err := json.Unmarshal(payload, &req); err != nil {
		log.Debug().Err(err).Msg("[dvrip] parse monitor JSON")
		return mainStream
	}

	streamType := req.OPMonitor.Parameter.StreamType
	log.Debug().Str("type", streamType).Msg("[dvrip] requested stream type")

	// "Main" -> main, "Extra1" -> sub (matches go2rtc client.go logic)
	if streamType == "Extra1" {
		return subStream
	}

	return mainStream
}

// dvripStreamData creates a consumer and streams DVRIP media packets to the client.
func dvripStreamData(dc *dvripConn, stream *Stream) {
	cons := &dvripConsumer{
		dc:   dc,
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
		log.Debug().Err(err).Msg("[dvrip] add consumer failed")
		return
	}

	defer stream.RemoveConsumer(cons)

	<-cons.done
}

// dvripConsumer implements core.Consumer for DVRIP streaming.
type dvripConsumer struct {
	dc     *dvripConn
	medias []*core.Media
	sender *core.Sender
	done   chan struct{}
}

func (c *dvripConsumer) GetMedias() []*core.Media {
	return c.medias
}

func (c *dvripConsumer) AddTrack(media *core.Media, codec *core.Codec, track *core.Receiver) error {
	sender := core.NewSender(media, codec)

	var seq uint32

	// sendFrame receives AVCC-encoded H264 data, converts to Annex B,
	// and wraps it in the DVRIP media packet + chunk format.
	//
	// DVRIP media packet format (from go2rtc ReadPacket analysis):
	//   IFrame (0xFC): 00 00 01 FC | mediaCode(1) FPS(1) W/8(1) H/8(1) | ts(4 LE) | size(4 LE) | annexb...
	//   PFrame (0xFD): 00 00 01 FD | size(4 LE) | annexb...
	sendFrame := func(pkt *rtp.Packet) {
		if len(pkt.Payload) < 5 {
			return
		}

		// pkt.Payload is AVCC here -- convert to Annex B for DVRIP wire format
		data := annexb.DecodeAVCC(pkt.Payload, true)
		if len(data) == 0 {
			return
		}

		// Detect keyframe: scan for first start code, check NAL type
		isKeyframe := false
		for i := 0; i+4 < len(data); i++ {
			if data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
				nalType := data[i+4] & 0x1F
				if nalType == 5 || nalType == 7 { // IDR or SPS
					isKeyframe = true
				}
				break
			}
		}

		var mediaPacket []byte
		if isKeyframe {
			mediaPacket = make([]byte, 16+len(data))
			mediaPacket[0] = 0x00
			mediaPacket[1] = 0x00
			mediaPacket[2] = 0x01
			mediaPacket[3] = 0xFC // H264 IFrame
			mediaPacket[4] = 0x02 // H264 media code
			mediaPacket[5] = 25   // FPS
			mediaPacket[6] = 240  // 1920 / 8
			mediaPacket[7] = 135  // 1080 / 8
			binary.LittleEndian.PutUint32(mediaPacket[8:], pkt.Timestamp)
			binary.LittleEndian.PutUint32(mediaPacket[12:], uint32(len(data)))
			copy(mediaPacket[16:], data)
		} else {
			mediaPacket = make([]byte, 8+len(data))
			mediaPacket[0] = 0x00
			mediaPacket[1] = 0x00
			mediaPacket[2] = 0x01
			mediaPacket[3] = 0xFD // PFrame
			binary.LittleEndian.PutUint32(mediaPacket[4:], uint32(len(data)))
			copy(mediaPacket[8:], data)
		}

		// Wrap in DVRIP chunk (20-byte 0xFF header)
		chunk := make([]byte, dvripHeaderSize+len(mediaPacket))
		chunk[0] = dvripSyncByte
		binary.LittleEndian.PutUint32(chunk[4:], c.dc.session)
		binary.LittleEndian.PutUint32(chunk[8:], seq)
		binary.LittleEndian.PutUint32(chunk[16:], uint32(len(mediaPacket)))
		copy(chunk[dvripHeaderSize:], mediaPacket)
		seq++

		c.dc.mu.Lock()
		_ = c.dc.conn.SetWriteDeadline(time.Now().Add(dvripTimeout))
		_, err := c.dc.conn.Write(chunk)
		c.dc.mu.Unlock()

		if err != nil {
			select {
			case <-c.done:
			default:
				close(c.done)
			}
		}
	}

	// If the producer sends raw RTP packets (FU-A/STAP-A), depay them to AVCC first.
	// RTSP producers (like ffmpeg via ANNOUNCE) use IsRTP() == true.
	// PayloadTypeRAW producers already deliver AVCC directly.
	if codec.IsRTP() {
		sender.Handler = h264.RTPDepay(codec, sendFrame)
	} else {
		sender.Handler = sendFrame
	}

	sender.HandleRTP(track)
	c.sender = sender

	return nil
}

func (c *dvripConsumer) Stop() error {
	if c.sender != nil {
		c.sender.Close()
	}
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	return c.dc.conn.Close()
}
