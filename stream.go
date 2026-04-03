package main

import (
	"sync"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/rs/zerolog/log"
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

	medias := prod.GetMedias()
	log.Debug().Int("medias", len(medias)).Str("stream", s.name).Msg("[stream] set producer")
	for _, media := range medias {
		log.Debug().Str("kind", media.Kind).Str("dir", media.Direction).Int("codecs", len(media.Codecs)).Msg("[stream] producer media")
		for _, codec := range media.Codecs {
			log.Debug().Str("codec", codec.Name).Uint32("clock", codec.ClockRate).Msg("[stream] producer codec")
			track, err := prod.GetTrack(media, codec)
			if err != nil {
				log.Debug().Err(err).Msg("[stream] get track failed")
				continue
			}
			s.receivers = append(s.receivers, track)
		}
	}
	log.Debug().Int("receivers", len(s.receivers)).Msg("[stream] receivers ready")
}

// AddConsumer connects a consumer to matching producer tracks.
func (s *Stream) AddConsumer(cons core.Consumer) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	consMedias := cons.GetMedias()
	log.Debug().Int("consMedias", len(consMedias)).Int("receivers", len(s.receivers)).Msg("[stream] add consumer")
	for _, consMedia := range consMedias {
		log.Debug().Str("kind", consMedia.Kind).Str("dir", consMedia.Direction).Int("codecs", len(consMedia.Codecs)).Msg("[stream] cons media")
		for _, receiver := range s.receivers {
			prodCodec := receiver.Codec
			if consMedia.Kind != core.GetKind(prodCodec.Name) {
				continue
			}
			// find consumer codec that matches producer codec
			// use prodCodec.Match(consCodec) because consumer codecs have ClockRate=0 (wildcard)
			var consCodec *core.Codec
			for _, cc := range consMedia.Codecs {
				if prodCodec.Match(cc) {
					consCodec = cc
					break
				}
			}
			if consCodec == nil {
				continue
			}
			log.Debug().Str("codec", consCodec.Name).Msg("[stream] matched, adding track")
			if err := cons.AddTrack(consMedia, consCodec, receiver); err != nil {
				log.Debug().Err(err).Msg("[stream] add track failed")
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
