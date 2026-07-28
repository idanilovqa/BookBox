#!/bin/sh
set -eu

umask 077

source_path=/run/secrets/postgres_password
destination_path=/run/bookbox-secrets/postgres_password
if [ ! -f "$source_path" ] || [ ! -r "$source_path" ]; then
    echo "required database secret source is unavailable" >&2
    exit 1
fi
temporary_path="${destination_path}.tmp.$$"
cp "$source_path" "$temporary_path"
chmod 0600 "$temporary_path"
mv "$temporary_path" "$destination_path"

exec /usr/local/bin/bookbox-catalog-worker "$@"
