FROM node:22-alpine AS web-build
WORKDIR /src
COPY web/package.json web/package-lock.json ./web/
RUN npm --prefix web ci
COPY web ./web
COPY internal/server/static ./internal/server/static
RUN npm --prefix web run build

FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
COPY --from=web-build /src/internal/server/static/ui ./internal/server/static/ui
RUN mkdir -p /out/adapters && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/integrated-recorder ./cmd/archiver && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/adapters/integrated-recorder-adapter-owncast ./cmd/adapters/owncast

FROM alpine:3.21
RUN apk add --no-cache ca-certificates ffmpeg && adduser -D -H -u 10001 archiver && mkdir -p /data && chown archiver:archiver /data
COPY --from=build /out/integrated-recorder /usr/local/bin/integrated-recorder
COPY --from=build /out/adapters/ /adapters/
USER 10001:10001
ENV ADDR=:8080 DATA_DIR=/data ADAPTER_DIR=/adapters:/external-adapters
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/integrated-recorder"]
