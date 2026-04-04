---
name: add-protocol-strixfakecam
description: Add emulation of a new camera protocol to StrixCamFake. Use when the user wants to add support for a new protocol like dvrip, bubble, webrtc, etc.
argument-hint: "[protocol-name]"
---

# Add a new protocol to StrixCamFake

The user wants to add emulation of protocol: `$ARGUMENTS`

## Architecture overview

StrixCamFake has two internal streams (`main` 1080p and `sub` 360p) fed by ffmpeg via RTSP ANNOUNCE. New protocols are added as **consumers** that read from these streams and serve data to external clients.

Key types:
- `Stream` (`stream.go`) -- holds producer + receivers, `AddConsumer()` connects a consumer
- `core.Consumer` interface -- from `github.com/AlexxIT/go2rtc/pkg/core`
- `core.Receiver` -- producer track that consumer subscribes to

## Step 1: Research the protocol in go2rtc

Before writing code, study how go2rtc implements the protocol:

1. Check if `pkg/<protocol>/` exists in go2rtc -- this is the reusable library
2. Check `internal/<protocol>/` -- this is the glue code showing how to wire it
3. Identify:
   - Does it need a TCP/UDP server? (like RTSP on :554, RTMP on :1935)
   - Or is it HTTP-based? (like HLS, MJPEG, FLV -- all on :80)
   - What consumer type does it use? (e.g., `flv.NewConsumer()`, `mp4.NewConsumer()`)
   - What URL patterns does it serve?

```bash
# Example: research dvrip
ls /home/user/go2rtc/pkg/dvrip/
cat /home/user/go2rtc/internal/dvrip/dvrip.go
```

Also check the StrixCamDB data for real camera URL patterns for this protocol.

## Step 2: Add the server code

Create a new file: `<protocol>_server.go` (for TCP-based) or add handlers to `http_server.go` (for HTTP-based).

### For TCP-based protocols (like RTSP, RTMP, DVRIP, Bubble):

```go
// <protocol>_server.go
package main

func start<Protocol>Server(port string, mainStream, subStream *Stream) {
    addr := ":" + port
    ln, err := net.Listen("tcp", addr)
    // ... accept loop, each connection in goroutine
}

func handle<Protocol>(conn net.Conn, mainStream, subStream *Stream) {
    // 1. Handshake / auth
    // 2. Determine main or sub from client request
    // 3. Create consumer: cons := <pkg>.NewConsumer()
    // 4. stream.AddConsumer(cons)
    // 5. Write data to client: cons.WriteTo(conn)
    // 6. defer stream.RemoveConsumer(cons)
}

func resolve<Protocol>Stream(request, main, sub *Stream) *Stream {
    // Map URL/path/channel to main or sub
}
```

### For HTTP-based protocols:

Add handler function in `http_server.go` and register in `startHTTPServer()`:

```go
mux.HandleFunc("/api/stream.<ext>", func(w http.ResponseWriter, r *http.Request) {
    handle<Protocol>Stream(w, r, mainStream, subStream)
})
```

### For protocols requiring ffmpeg transcoding (like MJPEG):

Use ffmpeg pipe pattern -- launch `ffmpeg -i rtsp://127.0.0.1:PORT/main -f <format> pipe:1` and pipe stdout to client. See `handleMJPEGStream()` as reference.

## Step 3: Add configuration

If the protocol needs its own port, add to `config.go`:

```go
type Config struct {
    // ... existing fields
    <Protocol>Port string
}

// In LoadConfig():
c.<Protocol>Port = env("<PROTOCOL>_PORT", "<default_port>")
```

Add to `.env.example`:
```
<PROTOCOL>_PORT=<default_port>
```

If the protocol uses the existing HTTP port (80) -- no config changes needed.

## Step 4: Wire it up in main.go

Add the server start call:

```go
// In main():
start<Protocol>Server(cfg.<Protocol>Port, mainStream, subStream)
```

Add to `printEndpoints()` to show available URLs.

If new port added, update `Dockerfile` EXPOSE and `docker-compose.yml` ports.

## Step 5: Add URL pattern routing

Study the URL patterns from StrixCamDB for this protocol. Add ALL known patterns to the resolve function. Map each to main or sub stream.

## Step 6: Build and test

```bash
go build ./...
```

Then use `/deploy-strixcam` to deploy and test on the server.

## Step 7: Commit via git

ALL changes go through git. NEVER copy files to server via SSH.

```bash
git add <new_files> <modified_files>
git commit -m "Add <protocol> protocol emulation

<describe what patterns are supported>"
git push origin main
```

## Checklist

- [ ] Protocol server/handler code
- [ ] URL pattern routing (all known camera patterns)
- [ ] Config (port if TCP-based)
- [ ] main.go wiring
- [ ] printEndpoints() updated
- [ ] .env.example updated (if new port)
- [ ] Dockerfile EXPOSE (if new port)
- [ ] docker-compose.yml ports (if new port)
- [ ] Builds successfully
- [ ] Tested on server via `/deploy-strixcam`

## Reference: existing protocol implementations

| File | Protocol | Type | Consumer |
|---|---|---|---|
| `rtsp_server.go` | RTSP | TCP :554 | `rtsp.Conn` (go2rtc built-in) |
| `rtmp_server.go` | RTMP | TCP :1935 | `flv.NewConsumer()` |
| `http_server.go` | MP4/HLS/FLV/TS/MJPEG | HTTP :80 | various from `pkg/` |
| `hls.go` | HLS | HTTP :80 | `mp4.NewConsumer()` or `mpegts.NewConsumer()` |
| `onvif.go` | ONVIF | HTTP :80 + UDP :3702 | N/A (metadata only) |
| `snapshot.go` | JPEG snapshots | ffmpeg pipe | N/A |
