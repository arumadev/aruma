#!/bin/sh
# Applies pending migrations before the server starts, so after an upgrade `docker compose up` is
# enough. Every other command (backup, accounts, version) runs as given.
set -e
if [ "$1" = serve ]; then
	aruma migrate up
fi
exec aruma "$@"
