#!/bin/sh
# Starts PostgreSQL in the background: at boot, and from Strife's kill
# nemesis after SIGKILL (crash recovery replays the WAL). Clients
# authenticate with SCRAM-SHA-256 and may use TLS (the image's self-signed
# certificate). Without STRIFE_PRIMARY the node is the primary; with it, an
# asynchronous hot standby of that primary.
set -eu
export PGDATA=/var/lib/postgresql/data
: "${STRIFE_PG_PASSWORD:?}"
if [ ! -s "$PGDATA/PG_VERSION" ]; then
  mkdir -p "$PGDATA" && chown postgres:postgres "$PGDATA" && chmod 700 "$PGDATA"
  if [ -z "${STRIFE_PRIMARY:-}" ]; then
    printf '%s\n' "$STRIFE_PG_PASSWORD" > /tmp/pw && chown postgres /tmp/pw
    gosu postgres initdb -D "$PGDATA" --auth-host=scram-sha-256 --auth-local=trust --pwfile=/tmp/pw >/dev/null
    rm -f /tmp/pw
    echo "host all all 0.0.0.0/0 scram-sha-256" >> "$PGDATA/pg_hba.conf"
    echo "host replication all 0.0.0.0/0 scram-sha-256" >> "$PGDATA/pg_hba.conf"
    cat >> "$PGDATA/postgresql.conf" <<CONF
listen_addresses = '*'
ssl = on
ssl_cert_file = '/etc/ssl/certs/ssl-cert-snakeoil.pem'
ssl_key_file = '/etc/ssl/private/ssl-cert-snakeoil.key'
max_connections = 200
CONF
  else
    until gosu postgres pg_isready -q -h "$STRIFE_PRIMARY"; do sleep 1; done
    PGPASSWORD="$STRIFE_PG_PASSWORD" gosu postgres pg_basebackup -h "$STRIFE_PRIMARY" -U postgres -D "$PGDATA" -X stream -R
  fi
fi
# After SIGKILL the pid file and socket lock remain; nothing else can be
# running in this container, so both are stale.
rm -f "$PGDATA/postmaster.pid" /var/run/postgresql/.s.PGSQL.*
touch /var/log/postgresql.log && chown postgres /var/log/postgresql.log
gosu postgres pg_ctl -D "$PGDATA" -l /var/log/postgresql.log -w start >/dev/null
