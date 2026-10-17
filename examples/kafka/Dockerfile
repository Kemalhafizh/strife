# Apache Kafka (KRaft) with the tools the Strife nemesis needs.
# PID 1 is a sleep so the broker can be killed and restarted by Strife.
ARG KAFKA_VERSION=3.9.1
FROM apache/kafka:${KAFKA_VERSION}
USER root
RUN apk add --no-cache iptables iproute2 procps
COPY start-kafka.sh /usr/local/bin/start-kafka.sh
RUN chmod +x /usr/local/bin/start-kafka.sh
ENTRYPOINT []
CMD ["sh", "-c", "/usr/local/bin/start-kafka.sh && exec sleep infinity"]
