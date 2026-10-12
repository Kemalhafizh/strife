# etcd with the tools the Strife nemesis needs (iptables, tc, pkill).
# PID 1 is a sleep so the database process can be killed and restarted
# without taking the container down.
FROM debian:bookworm-slim
ARG ETCD_VERSION=v3.5.17
ARG TARGETARCH=amd64
RUN apt-get update \
 && apt-get install -y --no-install-recommends iptables iproute2 procps curl ca-certificates \
 && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL "https://github.com/etcd-io/etcd/releases/download/${ETCD_VERSION}/etcd-${ETCD_VERSION}-linux-${TARGETARCH}.tar.gz" \
    | tar xz -C /tmp \
 && mv /tmp/etcd-${ETCD_VERSION}-linux-${TARGETARCH}/etcd /tmp/etcd-${ETCD_VERSION}-linux-${TARGETARCH}/etcdctl /usr/local/bin/ \
 && rm -rf /tmp/etcd-*
COPY start-etcd.sh /usr/local/bin/start-etcd.sh
RUN chmod +x /usr/local/bin/start-etcd.sh
CMD ["sh", "-c", "/usr/local/bin/start-etcd.sh && exec sleep infinity"]
