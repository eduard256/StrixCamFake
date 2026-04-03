FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
RUN CGO_ENABLED=0 go build -o /strixcamfake .

FROM alpine:3.21

RUN apk add --no-cache ffmpeg wget

WORKDIR /app

# download video assets from GitHub Release
ARG RELEASE_URL=https://github.com/eduard256/StrixCamFake/releases/download/v0.0.1-assets
RUN wget -q "${RELEASE_URL}/main.mp4" -O main.mp4 && \
    wget -q "${RELEASE_URL}/sub.mp4" -O sub.mp4

COPY --from=builder /strixcamfake /app/strixcamfake

EXPOSE 554 80 1935 34568 3702/udp

CMD ["/app/strixcamfake"]
