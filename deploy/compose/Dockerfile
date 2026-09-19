# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /src

ENV GOPROXY=https://goproxy.cn,direct

# Copy dependency files
COPY go.mod go.sum ./
RUN go mod download

# Copy source tree
COPY . .

# Build talkintent CLI and mockllm binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/talkintent ./cmd/talkintent && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /bin/mockllm ./test/mockllm/cmd/mockllm

# Final runtime stage: minimal alpine with git, curl, bash, jq
FROM alpine:3.20

RUN apk add --no-cache git bash curl jq ca-certificates openssh-client

# Create non-root talkintent user and home structure with writeable data dirs
RUN addgroup -S talkintent && adduser -S talkintent -G talkintent -h /home/talkintent && \
    mkdir -p /data /shared/invites /shared/hub-data && \
    chown -R talkintent:talkintent /data /shared /home/talkintent
USER talkintent
WORKDIR /home/talkintent

# Copy binaries from builder
COPY --from=builder /bin/talkintent /usr/local/bin/talkintent
COPY --from=builder /bin/mockllm /usr/local/bin/mockllm

ENTRYPOINT ["/usr/local/bin/talkintent"]
CMD ["version"]
