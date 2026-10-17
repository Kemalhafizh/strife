# PostgreSQL 17 with the tools the Strife nemesis needs (iptables, tc, pkill).
# PID 1 is a sleep so the server can be killed and restarted by the nemesis.
FROM postgres:17-bookworm
RUN apt-get update \
 && apt-get install -y --no-install-recommends iptables iproute2 procps \
 && rm -rf /var/lib/apt/lists/*
COPY start-postgres.sh /usr/local/bin/start-postgres.sh
RUN chmod +x /usr/local/bin/start-postgres.sh
ENTRYPOINT []
CMD ["sh", "-c", "/usr/local/bin/start-postgres.sh && exec sleep infinity"]
