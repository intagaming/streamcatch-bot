FROM nvidia/cuda:12.6.1-base-ubuntu24.04

COPY --from=golang:1.23.0-bookworm /usr/local/go/ /usr/local/go/
ENV PATH="/usr/local/go/bin:${PATH}"

RUN apt update
RUN apt install -y pipx ffmpeg
RUN rm -rf /var/lib/apt/lists/*

RUN pipx install streamlink
ENV PATH="/root/.local/bin:${PATH}"

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build .

ARG DEV
ARG APP_ID
ARG BOT_TOKEN
ARG MEDIA_SERVER_API_URL
ARG MEDIA_SERVER_HLS_URL
ARG MEDIA_SERVER_PLAYBACK_URL
ARG RECORDING_URL
ARG MEDIA_SERVER_PUBLISH_USER
ARG MEDIA_SERVER_PUBLISH_PASSWORD
ARG MEDIA_SERVER_RTSP_HOST
ARG TWITCH_AUTH_TOKEN
ARG TWITCH_CLIENT_ID
ARG TWITCH_CLIENT_SECRET
ARG REDIS_ADDR
ARG REDIS_PASSWORD
ARG NVIDIA_GPU

ENV DEV=DEV
ENV APP_ID=APP_ID
ENV BOT_TOKEN=BOT_TOKEN
ENV MEDIA_SERVER_API_URL=MEDIA_SERVER_API_URL
ENV MEDIA_SERVER_HLS_URL=MEDIA_SERVER_HLS_URL
ENV MEDIA_SERVER_PLAYBACK_URL=MEDIA_SERVER_PLAYBACK_URL
ENV RECORDING_URL=RECORDING_URL
ENV MEDIA_SERVER_PUBLISH_USER=MEDIA_SERVER_PUBLISH_USER
ENV MEDIA_SERVER_PUBLISH_PASSWORD=MEDIA_SERVER_PUBLISH_PASSWORD
ENV MEDIA_SERVER_RTSP_HOST=MEDIA_SERVER_RTSP_HOST
ENV TWITCH_AUTH_TOKEN=TWITCH_AUTH_TOKEN
ENV TWITCH_CLIENT_ID=TWITCH_CLIENT_ID
ENV TWITCH_CLIENT_SECRET=TWITCH_CLIENT_SECRET
ENV REDIS_ADDR=REDIS_ADDR
ENV REDIS_PASSWORD=REDIS_PASSWORD
ENV NVIDIA_GPU=NVIDIA_GPU
ENV NVIDIA_DRIVER_CAPABILITIES=all

ENTRYPOINT ["/app/streamcatch-bot"]
