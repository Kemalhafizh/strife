# Apache Kafka with nemesis tools (iptables, tc, pkill).
# PID 1 is sleep infinity so Kafka can be killed and restarted by Strife.
FROM apache/kafka:3.7.0
USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends iptables iproute2 procps \
 && rm -rf /var/lib/apt/lists/*
COPY start-kafka.sh /usr/local/bin/start-kafka.sh
RUN chmod +x /usr/local/bin/start-kafka.sh
ENTRYPOINT []
CMD ["sh", "-c", "/usr/local/bin/start-kafka.sh && exec sleep infinity"]
