package main

import (
	"sync"

	"github.com/AlexxIT/go2rtc/pkg/core"
)

// Stream connects one Producer (ffmpeg) to many Consumers (RTSP/HTTP/RTMP clients).
// Simplified version of go2rtc internal/streams -- we only need one producer per stream.
type Stream struct {
	name string

	mu        sync.Mutex
	prod      core.Producer
	receivers []*core.Receiver
	consumers []core.Consumer
}

func NewStream(name string) *Stream {
	return &Stream{name: name}
}

// SetProducer assigns a producer and extracts its receivers.
func (s *Stream) SetProducer(prod core.Producer) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.prod = prod
	s.receivers = nil

	for _, media := range prod.GetMedias() {
		for _, codec := range media.Codecs {
			track, err := prod.GetTrack(media, codec)
			if err != nil {
				continue
			}
			s.receivers = append(s.receivers, track)
		}
	}
}

// AddConsumer connects a consumer to matching producer tracks.
func (s *Stream) AddConsumer(cons core.Consumer) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, consMedia := range cons.GetMedias() {
		for _, receiver := range s.receivers {
			prodCodec := receiver.Codec
			// match by kind (video/audio)
			if consMedia.Kind != core.GetKind(prodCodec.Name) {
				continue
			}
			consCodec := consMedia.MatchCodec(prodCodec)
			if consCodec == nil {
				continue
			}
			if err := cons.AddTrack(consMedia, consCodec, receiver); err != nil {
				continue
			}
			break
		}
	}

	s.consumers = append(s.consumers, cons)
	return nil
}

func (s *Stream) RemoveConsumer(cons core.Consumer) {
	_ = cons.Stop()

	s.mu.Lock()
	for i, c := range s.consumers {
		if c == cons {
			s.consumers = append(s.consumers[:i], s.consumers[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
}

func (s *Stream) HasProducer() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prod != nil
}
