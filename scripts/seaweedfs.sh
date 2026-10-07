#!/usr/bin/env bash
# seaweedfs starts or stops the acceptance harness: one SeaweedFS container
# running `weed mini`, with only its S3 gateway published, on 127.0.0.1 at
# SEAWEEDFS_PORT (default 8333). Run it through `mise run seaweedfs:start` and
# `mise run seaweedfs:stop`.
#
# The gateway's admin identity is the access key admin with the secret
# secret, which SeaweedFS reads from AWS_ACCESS_KEY_ID and
# AWS_SECRET_ACCESS_KEY at startup. -s3.autoCreateBucket=false turns off
# SeaweedFS's default of creating a missing bucket on an admin's upload, so a
# write to a missing bucket fails as it does on AWS.
#
# start waits until a request signed with that identity lists the buckets,
# which proves the gateway, its filer, and the credential together; the
# gateway answers unsigned requests before the credential is loaded. The
# container keeps its data inside itself and is removed when it stops, so
# every start begins empty.
set -euo pipefail

image=chrislusf/seaweedfs:4.48
name=spike-s3-seaweedfs
port=${SEAWEEDFS_PORT:-8333}
endpoint=http://127.0.0.1:$port
wait_seconds=60

start() {
	if [ -n "$(docker ps -q --filter "name=^${name}$")" ]; then
		echo "seaweedfs: $name is already running"
	else
		docker run --detach --rm --name "$name" \
			--publish "127.0.0.1:$port:8333" \
			--env AWS_ACCESS_KEY_ID=admin \
			--env AWS_SECRET_ACCESS_KEY=secret \
			"$image" mini -dir=/data -s3.autoCreateBucket=false >/dev/null
	fi

	for _ in $(seq "$wait_seconds"); do
		code=$(curl --silent --output /dev/null --write-out '%{http_code}' \
			--aws-sigv4 "aws:amz:us-east-1:s3" --user admin:secret \
			"$endpoint/" || true)
		if [ "$code" = 200 ]; then
			echo "seaweedfs: S3 gateway ready at $endpoint"
			return 0
		fi
		sleep 1
	done
	echo "seaweedfs: S3 gateway not ready at $endpoint after ${wait_seconds}s; container log:" >&2
	docker logs --tail 50 "$name" >&2 || true
	return 1
}

stop() {
	if [ -n "$(docker ps -aq --filter "name=^${name}$")" ]; then
		docker stop "$name" >/dev/null
		echo "seaweedfs: stopped $name"
	else
		echo "seaweedfs: $name is not running"
	fi
}

case "${1:-}" in
start) start ;;
stop) stop ;;
*)
	echo "usage: $0 start|stop" >&2
	exit 2
	;;
esac
