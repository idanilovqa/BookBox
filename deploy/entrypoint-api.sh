#!/bin/sh
set -eu

umask 077

copy_secret() {
    source_path=$1
    destination_path=$2
    if [ ! -f "$source_path" ] || [ ! -r "$source_path" ]; then
        echo "required secret source is unavailable: $source_path" >&2
        exit 1
    fi
    temporary_path="${destination_path}.tmp.$$"
    cp "$source_path" "$temporary_path"
    chmod 0600 "$temporary_path"
    mv "$temporary_path" "$destination_path"
}

copy_secret /run/secrets/app_password_hash.source /run/netstorage-secrets/app_password_hash
copy_secret /run/secrets/session_key.source /run/netstorage-secrets/session_key
copy_secret /run/secrets/postgres_password /run/netstorage-secrets/postgres_password

exec /usr/local/bin/netstorage-api -config /etc/netstorage/config.yml "$@"
