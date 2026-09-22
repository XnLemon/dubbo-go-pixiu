# Backend API Documentation

**English** | [中文](API_CN.md)

The API Router configuration is managed through the `AdminRouteBinding` lifecycle. A route is saved as a draft first, then validated, diffed, and published independently. Publishing or deleting one route does not publish or delete other routes.

More detailed API descriptions are available in the [Swagger documentation](./doc/swagger.json).

## Response Codes

* `10001`: Success
* `10002`: Data not found
* `10003`: Concurrent operation; refresh and try again

## Basic Information

```http
GET /config/api/base
POST /config/api/base/
PUT /config/api/base/
```

The write endpoints accept YAML in the form field `content`.

## API Routes

### List and inspect routes

```http
GET /config/api/route/list?scope=draft
GET /config/api/route/list?scope=published
GET /config/api/route/detail?name=<route-name>&scope=draft
GET /config/api/route/status?name=<route-name>
GET /config/api/route/diff?name=<route-name>
```

The draft list includes the backend-calculated publish status for every route.

### Save a draft

```http
POST /config/api/route
PUT /config/api/route
```

Send an `AdminRouteBinding` JSON object. Updates may include `expectedRevision` for optimistic concurrency control:

```json
{
  "object": {
    "kind": "AdminRouteBinding",
    "metadata": {"name": "user-get"},
    "spec": {}
  },
  "expectedRevision": 12
}
```

### Validate and preview

```http
POST /config/api/route/validate
POST /config/api/route/preview
```

These endpoints do not write configuration. Validation applies schema defaults; preview returns the generated legacy Pixiu YAML.

### Publish or delete one route

```http
PUT    /config/api/route/publish?name=<route-name>&expectedRevision=<revision>
DELETE /config/api/route?name=<route-name>&expectedRevision=<revision>
```

Both operations are independent per route and use one etcd transaction for the Admin binding and generated runtime configuration. There is no full-configuration publish endpoint.
