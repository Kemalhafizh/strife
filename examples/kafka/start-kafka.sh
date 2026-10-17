#!/bin/sh
# Starts a combined KRaft broker+controller in the background. Used at boot
# and by Strife's kill nemesis to restart the broker after SIGKILL. The log
# and metadata survive restarts.
#
# Durability settings are the ones Kafka needs for acks=all to mean
# "replicated": replication factor 3, min.insync.replicas 2 and no unclean
# leader election.
set -eu
DATA=/var/lib/strife-kafka
CFG=$DATA/server.properties
mkdir -p "$DATA/log"
cat > "$CFG" <<PROPS
node.id=${STRIFE_NODE_ID:?}
process.roles=broker,controller
listeners=PLAINTEXT://0.0.0.0:9092,CONTROLLER://0.0.0.0:9093
advertised.listeners=PLAINTEXT://${STRIFE_IP:?}:9092
listener.security.protocol.map=PLAINTEXT:PLAINTEXT,CONTROLLER:PLAINTEXT
controller.listener.names=CONTROLLER
inter.broker.listener.name=PLAINTEXT
controller.quorum.voters=${STRIFE_VOTERS:?}
log.dirs=$DATA/log
num.partitions=1
default.replication.factor=3
min.insync.replicas=2
unclean.leader.election.enable=false
auto.create.topics.enable=true
offsets.topic.replication.factor=3
transaction.state.log.replication.factor=3
transaction.state.log.min.isr=2
group.initial.rebalance.delay.ms=0
PROPS
if [ ! -f "$DATA/log/meta.properties" ]; then
  /opt/kafka/bin/kafka-storage.sh format --ignore-formatted -t "${STRIFE_CLUSTER_ID:?}" -c "$CFG" >/dev/null
fi
nohup /opt/kafka/bin/kafka-server-start.sh "$CFG" >>/var/log/kafka.log 2>&1 &
