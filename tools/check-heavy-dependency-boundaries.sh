#!/usr/bin/env bash
# check-heavy-dependency-boundaries.sh - keep optional SDK deps isolated.
#
# The direct dependency allowlist approves who may enter the trust set. This
# guard approves where heavier optional SDKs may live, so generic modules do
# not quietly start pulling Redis, pgx, cloud, KMS/Vault, messaging, or
# testcontainer dependency clusters.
set -euo pipefail

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

direct="$tmpdir/direct"
violations="$tmpdir/violations"
: > "$violations"

git ls-files -co --exclude-standard -- '*go.mod' |
    while IFS= read -r gomod; do
        awk '
            function emit(module) {
                if (module != "") {
                    print module "\t" FILENAME ":" FNR
                }
            }
            /^require[[:space:]]+\(/ { inreq=1; next }
            inreq && /^\)/ { inreq=0; next }
            inreq {
                if ($0 !~ /\/\/[[:space:]]*indirect/ && $1 !~ /^\/\//) {
                    emit($1)
                }
                next
            }
            /^require[[:space:]]+/ {
                if ($0 !~ /\/\/[[:space:]]*indirect/) {
                    emit($2)
                }
            }
        ' "$gomod"
    done |
    sort -u > "$direct"

allowed_for_boundary_dep() {
    local gomod="$1"
    local dep="$2"

    case "$dep" in
        github.com/redis/go-redis/v9|github.com/alicebob/miniredis/v2|github.com/bds421/rho-kit/infra/redis/v2|github.com/bds421/rho-kit/infra/redis/redistest/v2)
            case "$gomod" in
                app/redis/go.mod|\
                data/budget/redis/go.mod|\
                data/cache/rediscache/go.mod|\
                data/idempotency/redisstore/go.mod|\
                data/lock/redislock/go.mod|\
                data/queue/redisqueue/go.mod|\
                data/ratelimit/redis/go.mod|\
                data/stream/redisstream/go.mod|\
                httpx/middleware/signedrequest/redis/go.mod|\
                infra/leaderelection/redislock/go.mod|\
                infra/messaging/redisbackend/go.mod|\
                infra/redis/go.mod|\
                infra/redis/redistest/go.mod|\
                */integrationtest/go.mod)
                    return 0
                    ;;
            esac
            return 1
            ;;

        github.com/jackc/pgx/v5|github.com/lib/pq|github.com/bds421/rho-kit/infra/sqldb/pgx/v2|github.com/bds421/rho-kit/infra/sqldb/dbtest/v2)
            case "$gomod" in
                app/postgres/go.mod|\
                cmd/kit-migrate/go.mod|\
                data/actionlog/postgres/go.mod|\
                data/approval/postgres/go.mod|\
                data/idempotency/pgstore/go.mod|\
                data/lock/pgadvisory/go.mod|\
                data/queue/riverqueue/go.mod|\
                infra/leaderelection/pgadvisory/go.mod|\
                infra/outbox/postgres/go.mod|\
                infra/sqldb/dbtest/go.mod|\
                infra/sqldb/pgx/go.mod|\
                observability/auditlog/postgres/go.mod|\
                */integrationtest/go.mod)
                    return 0
                    ;;
            esac
            return 1
            ;;

        github.com/riverqueue/river|github.com/riverqueue/river/riverdriver/riverpgxv5|github.com/riverqueue/river/rivertype|github.com/bds421/rho-kit/data/queue/riverqueue/v2)
            case "$gomod" in
                data/queue/riverqueue/go.mod|\
                data/queue/riverqueue/integrationtest/go.mod)
                    return 0
                    ;;
            esac
            return 1
            ;;

        github.com/rabbitmq/amqp091-go|github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2)
            case "$gomod" in
                app/amqp/go.mod|\
                infra/messaging/amqpbackend/go.mod|\
                infra/messaging/amqpbackend/integrationtest/go.mod)
                    return 0
                    ;;
            esac
            return 1
            ;;

        github.com/nats-io/nats.go|github.com/bds421/rho-kit/infra/messaging/natsbackend/v2)
            case "$gomod" in
                app/nats/go.mod|\
                infra/messaging/natsbackend/go.mod|\
                infra/messaging/natsbackend/integrationtest/go.mod)
                    return 0
                    ;;
            esac
            return 1
            ;;

        github.com/segmentio/kafka-go|github.com/bds421/rho-kit/infra/messaging/kafkabackend/v2)
            case "$gomod" in
                infra/messaging/kafkabackend/go.mod|\
                infra/messaging/kafkabackend/integrationtest/go.mod)
                    return 0
                    ;;
            esac
            return 1
            ;;

        github.com/bds421/rho-kit/infra/messaging/redisbackend/v2)
            case "$gomod" in
                infra/messaging/redisbackend/go.mod|*/integrationtest/go.mod)
                    return 0
                    ;;
            esac
            return 1
            ;;

        github.com/openfga/go-sdk|github.com/bds421/rho-kit/authz/openfga/v2)
            case "$gomod" in
                authz/openfga/go.mod|*/integrationtest/go.mod)
                    return 0
                    ;;
            esac
            return 1
            ;;

        github.com/open-feature/go-sdk)
            case "$gomod" in
                flags/go.mod|*/integrationtest/go.mod)
                    return 0
                    ;;
            esac
            return 1
            ;;

        github.com/aws/aws-sdk-go-v2|github.com/aws/aws-sdk-go-v2/service/kms|github.com/bds421/rho-kit/crypto/envelope/awskms/v2)
            case "$gomod" in
                crypto/envelope/awskms/go.mod|\
                infra/storage/s3backend/go.mod|\
                */integrationtest/go.mod)
                    return 0
                    ;;
            esac
            return 1
            ;;

        github.com/aws/aws-sdk-go-v2/config|github.com/aws/aws-sdk-go-v2/credentials|github.com/aws/aws-sdk-go-v2/service/s3|github.com/bds421/rho-kit/infra/storage/s3backend/v2)
            case "$gomod" in
                infra/storage/s3backend/go.mod|\
                infra/storage/storagetest/go.mod|\
                */integrationtest/go.mod)
                    return 0
                    ;;
            esac
            return 1
            ;;

        cloud.google.com/go/kms|github.com/bds421/rho-kit/crypto/envelope/gcpkms/v2)
            case "$gomod" in
                crypto/envelope/gcpkms/go.mod|*/integrationtest/go.mod)
                    return 0
                    ;;
            esac
            return 1
            ;;

        github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azkeys|github.com/bds421/rho-kit/crypto/envelope/azurekeyvault/v2)
            case "$gomod" in
                crypto/envelope/azurekeyvault/go.mod|*/integrationtest/go.mod)
                    return 0
                    ;;
            esac
            return 1
            ;;

        github.com/hashicorp/vault/api|github.com/bds421/rho-kit/crypto/envelope/vaulttransit/v2)
            case "$gomod" in
                crypto/envelope/vaulttransit/go.mod|*/integrationtest/go.mod)
                    return 0
                    ;;
            esac
            return 1
            ;;

        cloud.google.com/go/storage|google.golang.org/api|github.com/bds421/rho-kit/infra/storage/gcsbackend/v2)
            case "$gomod" in
                infra/storage/gcsbackend/go.mod|*/integrationtest/go.mod)
                    return 0
                    ;;
            esac
            return 1
            ;;

        github.com/Azure/azure-sdk-for-go/sdk/storage/azblob|github.com/bds421/rho-kit/infra/storage/azurebackend/v2)
            case "$gomod" in
                infra/storage/azurebackend/go.mod|*/integrationtest/go.mod)
                    return 0
                    ;;
            esac
            return 1
            ;;

        github.com/pkg/sftp|github.com/bds421/rho-kit/infra/storage/sftpbackend/v2)
            case "$gomod" in
                infra/storage/sftpbackend/go.mod|\
                infra/storage/storagetest/go.mod|\
                */integrationtest/go.mod)
                    return 0
                    ;;
            esac
            return 1
            ;;

        github.com/testcontainers/testcontainers-go|github.com/testcontainers/testcontainers-go/modules/*)
            case "$gomod" in
                infra/redis/redistest/go.mod|\
                infra/sqldb/dbtest/go.mod|\
                infra/storage/storagetest/go.mod|\
                */integrationtest/go.mod)
                    return 0
                    ;;
            esac
            return 1
            ;;

        k8s.io/api|k8s.io/apimachinery|k8s.io/client-go|github.com/bds421/rho-kit/infra/leaderelection/k8slease/v2)
            case "$gomod" in
                infra/leaderelection/k8slease/go.mod|\
                */integrationtest/go.mod)
                    return 0
                    ;;
            esac
            return 1
            ;;
    esac

    return 0
}

while IFS=$'\t' read -r dep location; do
    gomod="${location%:*}"
    if ! allowed_for_boundary_dep "$gomod" "$dep"; then
        printf '%s: %s is restricted to its adapter/composition-root/test modules\n' "$location" "$dep" >> "$violations"
    fi
done < "$direct"

if [[ -s "$violations" ]]; then
    echo "heavy dependency boundary check FAILED" >&2
    sed 's/^/  /' "$violations" >&2
    printf '\nMove the import behind an adapter-specific module, or update this gate with reviewer sign-off if the new boundary is intentional.\n' >&2
    exit 1
fi

count=$(wc -l < "$direct" | tr -d ' ')
echo "heavy dependency boundary check OK (${count} direct module edges checked)"
