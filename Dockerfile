# Build stage.
#
# The Go version is pinned to the one in go.mod rather than to "latest", so that
# a rebuild months from now produces the binary this was verified with.
FROM golang:1.26.2-alpine AS build

WORKDIR /src

# Dependencies are copied on their own so that editing source does not re-download
# the module cache on every rebuild.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO is off so the binary runs on a base image with no libc.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ai-o11y-server ./cmd/ai-o11y-server

# Runtime stage.
#
# alpine rather than scratch: the service reaches the metrics backend and the
# inventories over TLS and needs the CA bundle.
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

RUN adduser -D -u 10001 aio11y
COPY --from=build /out/ai-o11y-server /usr/local/bin/ai-o11y-server
USER aio11y
EXPOSE 8094

CMD ["/usr/local/bin/ai-o11y-server"]
