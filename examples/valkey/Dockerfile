# Valkey with the tools the Strife nemesis needs (iptables, tc, pkill).
# PID 1 is a sleep so the server can be killed and restarted without taking
# the container down.
FROM valkey/valkey:8.0
RUN apt-get update \
 && apt-get install -y --no-install-recommends iptables iproute2 procps \
 && rm -rf /var/lib/apt/lists/*
COPY start-valkey.sh /usr/local/bin/start-valkey.sh
RUN chmod +x /usr/local/bin/start-valkey.sh
ENTRYPOINT []
CMD ["sh", "-c", "/usr/local/bin/start-valkey.sh && exec sleep infinity"]
