#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
container_name="sqlglot-database-tests-$$"
started=false
cleanup() {
  if [ "$started" = true ]
  then
    docker rm --force --volumes "$container_name" >/dev/null
  fi
}
trap cleanup EXIT

GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH="${SQLGLOT_DOCKER_ARCH:-amd64}" make build-database-tests

case "${1:-}" in
  postgres)
    docker run --detach --name "$container_name" --network none --env POSTGRES_HOST_AUTH_METHOD=trust postgres:17-alpine >/dev/null
    started=true
    docker cp .tmp/database-review.test "$container_name:/database-review.test"
    ready=false
    for attempt in {1..60}
    do
      if docker exec "$container_name" pg_isready --quiet
      then
        ready=true
        break
      fi
      sleep 1
    done
    if [ "$ready" != true ]
    then
      docker logs "$container_name"
      exit 1
    fi
    docker exec --env 'SQLGLOT_TEST_POSTGRES_URL=postgres://postgres@127.0.0.1/postgres?sslmode=disable' "$container_name" /database-review.test -test.v
    ;;
  sqlserver)
    test_password="Db$(openssl rand -hex 24)!"
    docker run --detach --name "$container_name" --network none --env ACCEPT_EULA=Y --env "MSSQL_SA_PASSWORD=$test_password" mcr.microsoft.com/mssql/server:2022-latest >/dev/null
    started=true
    docker cp .tmp/database-sqlserver.test "$container_name:/database-sqlserver.test"
    docker cp .tmp/database-bundle.test "$container_name:/database-bundle.test"
    ready=false
    for attempt in {1..60}
    do
      if docker exec --env "SQLCMDPASSWORD=$test_password" "$container_name" /opt/mssql-tools18/bin/sqlcmd -S tcp:127.0.0.1,1433 -U sa -C -l 2 -t 2 -Q 'SELECT 1' >/dev/null 2>&1
      then
        ready=true
        break
      fi
      sleep 1
    done
    if [ "$ready" != true ]
    then
      docker logs "$container_name"
      exit 1
    fi
    docker exec --env "COMMONS_DB_SQLSERVER_ARTIFACT_TEST_URL=sqlserver://sa:$test_password@127.0.0.1:1433?database=master&encrypt=disable" "$container_name" /database-sqlserver.test -test.v
    docker exec --env "COMMONS_DB_SQLSERVER_URL=sqlserver://sa:$test_password@127.0.0.1:1433?encrypt=disable" "$container_name" /database-bundle.test -test.v
    ;;
  *)
    echo 'usage: bash hack/database-integration.sh postgres|sqlserver' >&2
    exit 2
    ;;
esac
