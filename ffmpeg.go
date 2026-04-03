package main

import (
	"fmt"
	"os/exec"
	"time"

	"github.com/rs/zerolog/log"
)

// startFFmpeg launches ffmpeg to push a looping MP4 file into the RTSP server via ANNOUNCE.
// It restarts automatically on failure.
func startFFmpeg(videoPath, streamName, rtspPort string) {
	url := fmt.Sprintf("rtsp://127.0.0.1:%s/%s", rtspPort, streamName)

	go func() {
		for {
			// -re: read input at native frame rate (realtime pacing)
			// -stream_loop -1: infinite loop
			// -c copy: no transcoding, just remux
			// -rtsp_transport tcp: use TCP interleaved for reliability
			// -f rtsp: output to RTSP via ANNOUNCE
			cmd := exec.Command("ffmpeg",
				"-re", "-stream_loop", "-1",
				"-i", videoPath,
				"-c", "copy",
				"-rtsp_transport", "tcp",
				"-f", "rtsp", url,
			)

			log.Info().Str("stream", streamName).Str("file", videoPath).Msg("[ffmpeg] starting")

			out, err := cmd.CombinedOutput()
			if err != nil {
				log.Error().Err(err).Str("stream", streamName).Bytes("output", lastN(out, 512)).Msg("[ffmpeg] exited")
			}

			// wait before restart to avoid CPU spin on persistent failure
			time.Sleep(3 * time.Second)
		}
	}()
}

func lastN(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[len(b)-n:]
}
