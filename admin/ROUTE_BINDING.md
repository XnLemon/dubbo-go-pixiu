<!--
 Licensed to the Apache Software Foundation (ASF) under one or more
 contributor license agreements.  See the NOTICE file distributed with
 this work for additional information regarding copyright ownership.
 The ASF licenses this file to You under the Apache License, Version 2.0
 (the "License"); you may not use this file except in compliance with
 the License.  You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

 Unless required by applicable law or agreed to in writing, software
 distributed under the License is distributed on an "AS IS" BASIS,
 WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 See the License for the specific language governing permissions and
 limitations under the License.
-->

# Schema-driven route bindings

Pixiu-Admin provides an initial vertical slice for managing one high-level
`AdminRouteBinding`. It describes one HTTP entry and one registry-backed Dubbo
method, while keeping the existing `Resource` and `Method` runtime format
unchanged.

## Lifecycle

```text
YAML / JSON editor
    -> server-side schema defaults and validation
    -> draft in the Admin control-plane namespace
    -> generated legacy YAML preview and diff
    -> publish transaction
    -> legacy resources/<id> and resources/<id>/method/<id>
    -> existing Pixiu runtime watcher
```

Saving a draft does not write the legacy runtime keys. A publish or rollback
stores the canonical record and history snapshot in one transaction, together
with the generated resource and method whenever the runtime projection changes
or has drifted. Re-publishing an unchanged object repairs missing or modified
owned runtime keys without creating another history revision. An unfinished
edit cannot change traffic, and an already-converged metadata-only publish does
not cause watcher churn.

## Example

```yaml
kind: AdminRouteBinding
metadata:
  name: user-get
spec:
  entry:
    protocol: http
    path: /api/v1/users/:id
    method: GET
  target:
    protocol: dubbo
    application: UserProvider
    interface: com.example.UserService
    method: GetUser
    version: 1.0.0
    group: stable
    cluster: user-dubbo
  params:
    - from: uri.id
      to: 0
      type: java.lang.String
  timeout: 2s
  publish:
    mode: draft
    validate: true
```

The server validates required fields, HTTP path and method, positive timeout,
URI placeholders, contiguous argument indexes, duplicate mappings, and the
supported scalar Dubbo parameter types. Defaults come from the built-in
schema registry rather than the page.

## Admin endpoints

All endpoints use the existing Admin response envelope (`code: 10001` for
success) and require the normal Admin token.

| Operation | Endpoint |
| --- | --- |
| Schema | `GET /config/api/route-binding/schema` |
| List | `GET /config/api/route-binding/list` |
| Detail | `GET /config/api/route-binding/detail?name=user-get` |
| Save draft | `POST /config/api/route-binding` with `content` YAML/JSON |
| Preview/Diff | `POST /config/api/route-binding/preview` with `content` YAML/JSON |
| Publish | `PUT /config/api/route-binding/publish` with `name` |
| History | `GET /config/api/route-binding/history?name=user-get` |
| Rollback | `POST /config/api/route-binding/rollback` with `name` and `revision` |
| Delete draft | `DELETE /config/api/route-binding?name=user-get` |

The Vue2 page is available from **网关配置 → 路由模型**. It intentionally uses
the existing page shell and request helpers; no frontend architecture change is
required for this proof of feasibility.

## Current boundary

Each object deliberately supports one HTTP entry to one Dubbo method; multiple
objects can use different runtime paths. The generated legacy method keeps
`target.application` and `target.cluster` as compatibility metadata, but they
do not yet select a provider or registry independently in the standard outbound
client. Route-specific registry selection and the remaining Resource, Listener,
Cluster, PluginGroup, RateLimiter, and OPA objects can be added on the same
registry/lifecycle boundary in a later phase.

Existing Resource/Method Admin endpoints and their direct YAML workflow remain
unchanged. ID allocation skips occupied legacy keys, and initial publish checks
key ownership again before writing, so a missing or stale legacy counter cannot
overwrite an existing route.

Publishing and rollback reject a route path that is equivalent to another
legacy Resource path (case-insensitive, including renamed URI placeholders).
This protects the current watcher, which associates Method records by
`resourcePath` and cannot safely maintain two Resource records for one runtime
route path.

The existing gateway loader now accepts a completely empty etcd prefix. It
loads a snapshot revision, starts the watch at the following revision, and
delays event delivery until the snapshot is installed in the local router. This
closes the clean-start and snapshot-to-watch gaps without changing the runtime
`Resource`/`Method` format. If the watch is canceled, compacted, or closed, the
loader takes a fresh snapshot, rebuilds the runtime routes, and resumes from the
new snapshot revision.

The lifecycle is serialized inside one Admin process. Running multiple Admin
writers requires an etcd compare-and-swap revision contract and is deliberately
left for the upstream immutable ConfigSet design.
