#!/bin/sh
# Starts postgres in the background. Used at boot and by Strife's kill
# nemesis to restart the server after SIGKILL.
set -eu
export PGDATA=/var/lib/postgresql/data

if [ -z "${STRIFE_PRIMARY:-}" ]; then
  # Primary node
  if [ ! -s "$PGDATA/PG_VERSION" ]; then
    gosu postgres initdb -D "$PGDATA" --auth-host=trust --auth-local=trust
    echo "host all all 0.0.0.0/0 trust" >> "$PGDATA/pg_hba.conf"
    echo "listen_addresses = '*'" >> "$PGDATA/postgresql.conf"
    echo "wal_level = replica" >> "$PGDATA/postgresql.conf"
    echo "max_wal_senders = 10" >> "$PGDATA/postgresql.conf"
  fi
else
  # Replica node
  if [ ! -s "$PGDATA/PG_VERSION" ]; then
    until gosu postgres pg_isready -h "$STRIFE_PRIMARY" -p 5432; do sleep 1; done
    gosu postgres pg_basebackup -h "$STRIFE_PRIMARY" -D "$PGDATA" -U postgres -Fp -Xs -R
  fi
fi

exec gosu postgres pg_ctl -D "$PGDATA" -w start
