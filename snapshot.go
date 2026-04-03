package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Snapshot periodically grabs a JPEG frame from the main stream using ffmpeg.
type Snapshot struct {
	mu   sync.RWMutex
	data []byte
}

func NewSnapshot() *Snapshot {
	return &Snapshot{}
}

func (s *Snapshot) Get() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data
}

func (s *Snapshot) Set(data []byte) {
	s.mu.Lock()
	s.data = data
	s.mu.Unlock()
}

// StartSnapshotLoop runs a goroutine that periodically captures a JPEG snapshot
// from the RTSP main stream using ffmpeg.
func StartSnapshotLoop(snap *Snapshot, rtspPort string, interval time.Duration) {
	url := "rtsp://127.0.0.1:" + rtspPort + "/main"

	go func() {
		// wait for stream to become available
		time.Sleep(5 * time.Second)

		for {
			data, err := captureSnapshot(url)
			if err != nil {
				log.Debug().Err(err).Msg("[snapshot] capture failed")
			} else if len(data) > 0 {
				snap.Set(data)
			}

			time.Sleep(interval)
		}
	}()
}

func captureSnapshot(rtspURL string) ([]byte, error) {
	// grab one frame from RTSP, output as JPEG to stdout
	cmd := exec.Command("ffmpeg",
		"-loglevel", "error",
		"-rtsp_transport", "tcp",
		"-i", rtspURL,
		"-frames:v", "1",
		"-q:v", "5",
		"-f", "image2",
		"-vcodec", "mjpeg",
		"pipe:1",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	_ = cmd.Run()

	if stdout.Len() > 0 {
		return stdout.Bytes(), nil
	}

	if stderr.Len() > 0 {
		return nil, fmt.Errorf("%s", stderr.String())
	}

	return nil, fmt.Errorf("no output")
}
