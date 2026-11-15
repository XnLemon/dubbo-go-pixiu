#!/bin/sh
#
# Licensed to the Apache Software Foundation (ASF) under one
# or more contributor license agreements.  See the NOTICE file
# distributed with this work for additional information
# regarding copyright ownership.  The ASF licenses this file
# to you under the Apache License, Version 2.0 (the
# "License"); you may not use this file except in compliance
# with the License.  You may obtain a copy of the License at
#
#   http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing,
# software distributed under the License is distributed on an
# "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
# KIND, either express or implied.  See the License for the
# specific language governing permissions and limitations
# under the License.
#

set -eu

cleanup() {
  status=$?
  trap - EXIT

  if [ "$status" -ne 0 ]; then
    docker compose ps || true
    docker compose logs --no-color 2>&1 \
      | sed -E 's/[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/[REDACTED JWT]/g' || true
  fi

  if ! docker compose down --remove-orphans; then
    if [ "$status" -eq 0 ]; then
      status=1
    fi
  fi

  exit "$status"
}
trap cleanup EXIT

docker compose config --quiet
docker compose up --build --detach --wait --wait-timeout 300

docker compose exec -T frontend npm run typecheck
docker compose exec -T frontend npm test
docker compose exec -T frontend npm run build

python3 .github/scripts/admin-compose-smoke.py
