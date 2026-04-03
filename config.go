package main

import (
	"os"
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
