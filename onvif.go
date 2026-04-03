package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/onvif"
	"github.com/rs/zerolog/log"
)

func handleONVIF(w http.ResponseWriter, r *http.Request, cfg *Config) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	operation := onvif.GetRequestAction(b)
	if operation == "" {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}

	log.Trace().Str("op", operation).Msg("[onvif] request")

	var resp []byte

	switch operation {
	case onvif.ServiceGetServiceCapabilities,
		onvif.DeviceGetNetworkInterfaces,
		onvif.DeviceGetSystemDateAndTime,
		onvif.DeviceSetSystemDateAndTime,
		onvif.DeviceGetDiscoveryMode,
		onvif.DeviceGetDNS,
		onvif.DeviceGetHostname,
		onvif.DeviceGetNetworkDefaultGateway,
		onvif.DeviceGetNetworkProtocols,
		onvif.DeviceGetNTP,
		onvif.DeviceGetScopes,
		onvif.MediaGetVideoEncoderConfiguration,
		onvif.MediaGetVideoEncoderConfigurations,
		onvif.MediaGetAudioEncoderConfigurations,
		onvif.MediaGetVideoEncoderConfigurationOptions,
		onvif.MediaGetAudioSources,
		onvif.MediaGetAudioSourceConfigurations:
		resp = onvif.StaticResponse(operation)

	case onvif.DeviceGetCapabilities:
		resp = onvif.GetCapabilitiesResponse(r.Host)

	case onvif.DeviceGetServices:
		resp = onvif.GetServicesResponse(r.Host)

	case onvif.DeviceGetDeviceInformation:
		resp = onvif.GetDeviceInformationResponse(
			"StrixCam", cfg.CameraModel, cfg.CameraFirmware, cfg.CameraSerial,
		)

	case onvif.MediaGetVideoSources:
		resp = onvif.GetVideoSourcesResponse([]string{"main", "sub"})

	case onvif.MediaGetProfiles:
		resp = onvif.GetProfilesResponse([]string{"main", "sub"})

	case onvif.MediaGetProfile:
		token := onvif.FindTagValue(b, "ProfileToken")
		resp = onvif.GetProfileResponse(token)

	case onvif.MediaGetVideoSourceConfigurations:
		resp = onvif.GetVideoSourceConfigurationsResponse([]string{"main", "sub"})

	case onvif.MediaGetVideoSourceConfiguration:
		token := onvif.FindTagValue(b, "ConfigurationToken")
		resp = onvif.GetVideoSourceConfigurationResponse(token)

	case onvif.MediaGetStreamUri:
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		token := onvif.FindTagValue(b, "ProfileToken")
		// return Hikvision-style RTSP URL based on profile token
		var ch string
		if token == "sub" {
			ch = "102"
		} else {
			ch = "101"
		}
		uri := fmt.Sprintf("rtsp://%s:%s/Streaming/Channels/%s", host, cfg.RTSPPort, ch)
		resp = onvif.GetStreamUriResponse(uri)

	case onvif.MediaGetSnapshotUri:
		uri := "http://" + r.Host + "/api/frame.jpeg"
		resp = onvif.GetSnapshotUriResponse(uri)

	default:
		http.Error(w, "unsupported: "+operation, http.StatusBadRequest)
		log.Debug().Str("op", operation).Msg("[onvif] unsupported")
		return
	}

	w.Header().Set("Content-Type", "application/soap+xml; charset=utf-8")
	_, _ = w.Write(resp)
}

// startWSDiscovery starts a WS-Discovery responder on UDP multicast 239.255.255.250:3702.
// When a Probe is received, it responds with a ProbeMatch advertising our ONVIF service.
func startWSDiscovery(httpPort, cameraName string) {
	addr := &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 3702}

	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		log.Error().Err(err).Msg("[onvif] ws-discovery listen failed")
		return
	}

	log.Info().Msg("[onvif] ws-discovery listening on 239.255.255.250:3702")

	deviceUUID := "urn:uuid:" + onvif.UUID()

	go func() {
		buf := make([]byte, 8192)
		for {
			n, remote, err := conn.ReadFromUDP(buf)
			if err != nil {
				log.Error().Err(err).Msg("[onvif] ws-discovery read")
				continue
			}

			msg := string(buf[:n])
			if !strings.Contains(msg, "Probe") {
				continue
			}

			// extract MessageID from request
			messageID := onvif.FindTagValue(buf[:n], "MessageID")
			if messageID == "" {
				continue
			}

			// determine our IP by looking at which interface the request came from
			localIP := getLocalIP(remote)
			if localIP == "" {
				continue
			}

			xaddrs := fmt.Sprintf("http://%s:%s/onvif/device_service", localIP, httpPort)

			response := buildProbeMatch(deviceUUID, messageID, xaddrs, cameraName)

			_, _ = conn.WriteToUDP([]byte(response), remote)

			log.Debug().Str("remote", remote.String()).Msg("[onvif] probe response sent")
		}
	}()
}

func buildProbeMatch(deviceUUID, relatesTo, xaddrs, cameraName string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
  xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing"
  xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery"
  xmlns:dn="http://www.onvif.org/ver10/network/wsdl">
  <s:Header>
    <a:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches</a:Action>
    <a:RelatesTo>` + relatesTo + `</a:RelatesTo>
    <a:To>http://schemas.xmlsoap.org/ws/2004/08/addressing/role/anonymous</a:To>
  </s:Header>
  <s:Body>
    <d:ProbeMatches>
      <d:ProbeMatch>
        <a:EndpointReference><a:Address>` + deviceUUID + `</a:Address></a:EndpointReference>
        <d:Types>dn:NetworkVideoTransmitter</d:Types>
        <d:Scopes>onvif://www.onvif.org/name/` + cameraName + ` onvif://www.onvif.org/hardware/StrixCamFake onvif://www.onvif.org/Profile/Streaming onvif://www.onvif.org/type/Network_Video_Transmitter</d:Scopes>
        <d:XAddrs>` + xaddrs + `</d:XAddrs>
        <d:MetadataVersion>1</d:MetadataVersion>
      </d:ProbeMatch>
    </d:ProbeMatches>
  </s:Body>
</s:Envelope>`
}

func getLocalIP(remote *net.UDPAddr) string {
	// connect UDP to the remote address to find the best local IP
	conn, err := net.DialTimeout("udp4", remote.String(), time.Second)
	if err != nil {
		// fallback: find first non-loopback IP
		return getFirstIP()
	}
	defer conn.Close()

	host, _, _ := net.SplitHostPort(conn.LocalAddr().String())
	return host
}

func getFirstIP() string {
	addrs, _ := net.InterfaceAddrs()
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			return ipNet.IP.String()
		}
	}
	return ""
}

// ensure core is imported (used by other files)
var _ = core.KindVideo
