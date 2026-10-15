FROM golang:1.23-bookworm AS builder

WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /strife-agent ./cmd/strife-agent

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    iproute2 \
    iptables \
    procps \
    ca-certificates \
    curl \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /strife-agent /usr/local/bin/strife-agent

EXPOSE 9292

ENTRYPOINT ["/usr/local/bin/strife-agent"]
CMD ["-listen", "0.0.0.0:9292"]
