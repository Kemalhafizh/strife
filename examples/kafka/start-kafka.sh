#!/bin/sh
# Starts Kafka broker in KRaft mode in the background. Used at boot
# and by Strife's kill nemesis to restart the broker.
set -eu
exec /opt/kafka/bin/kafka-server-start.sh -daemon /opt/kafka/config/kraft/server.properties
