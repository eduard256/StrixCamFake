package main

import (
	"os"
	"strings"
	"time"
)

// Config holds all application settings loaded from environment variables.
type Config struct {
	MainVideo string
	SubVideo  string

	Username string
	Password string

	RTSPPort   string
	HTTPPort   string
	RTMPPort   string
	BubblePort string
	DVRIPPort  string

	CameraName     string
	CameraModel    string
	CameraSerial   string
	CameraFirmware string

	// ONVIFEnabled controls whether ONVIF is exposed at all.
	// When false: the /onvif/ SOAP endpoint is not registered on the HTTP
	// server and the WS-Discovery multicast responder is not started, so the
	// camera appears to have no ONVIF support from the network's point of view.
	ONVIFEnabled bool

	SnapshotInterval time.Duration
}

func LoadConfig() *Config {
	c := &Config{
		MainVideo:        env("MAIN_VIDEO", "main.mp4"),
		SubVideo:         env("SUB_VIDEO", "sub.mp4"),
		Username:         env("USERNAME", "admin"),
		Password:         env("PASSWORD", "admin"),
		RTSPPort:         env("RTSP_PORT", "554"),
		HTTPPort:         env("HTTP_PORT", "80"),
		RTMPPort:         env("RTMP_PORT", "1935"),
		BubblePort:       env("BUBBLE_PORT", "34567"),
		DVRIPPort:        env("DVRIP_PORT", "34568"),
		CameraName:       env("CAMERA_NAME", "StrixCam"),
		CameraModel:      env("CAMERA_MODEL", "SFC-2000"),
		CameraSerial:     env("CAMERA_SERIAL", "SFC-001"),
		CameraFirmware:   env("CAMERA_FIRMWARE", "1.0.0"),
		ONVIFEnabled:     envBool("ONVIF_ENABLED", true),
		SnapshotInterval: 5 * time.Second,
	}

	if s := os.Getenv("SNAPSHOT_INTERVAL"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			c.SnapshotInterval = d
		}
	}

	return c
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envBool parses a boolean environment variable.
// Accepted truthy values: "1", "true", "yes", "on" (case-insensitive).
// Accepted falsy values: "0", "false", "no", "off" (case-insensitive).
// Empty or unrecognized values fall back to the provided default, so typos
// keep the safer default behavior instead of silently flipping the flag.
func envBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}
