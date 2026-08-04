# RemoteCLI API

All execution and artifact requests require:

```http
Authorization: Bearer <token>
```

## Health

```http
GET /healthz
```

Returns service metadata without authentication:

```json
{"ok":true,"service":"remotecli","version":"0.1.0"}
```

## Execute OpenCLI

```http
POST /v1/execute
Content-Type: application/json

{"args":["list","-f","json"],"artifacts":true}
```

`args` is passed to the local OpenCLI executable as an argument array. It is
never interpreted by a shell. `timeoutMs` may request a shorter timeout than
the server limit; `artifacts` controls whether files in the request workspace
are retained and listed.

Successful process execution returns HTTP 200 even when OpenCLI exits with a
non-zero code:

```json
{
  "ok": false,
  "runId": "e2d8...",
  "exitCode": 77,
  "stdout": "",
  "stderr": "Not logged in\n",
  "durationMs": 1200,
  "artifacts": []
}
```

API failures use an error envelope and a non-2xx status:

```json
{"error":{"code":"unauthorized","message":"valid Bearer token required"}}
```

Common statuses are 400 for invalid requests, 401 for authentication failure,
503 for unavailable OpenCLI, and 504 for the configured command timeout.

## Download an artifact

The execute response contains artifact metadata and a relative URL:

```json
{
  "id":"a1b2...",
  "path":"xhs/image.jpg",
  "size":12345,
  "sha256":"...",
  "mediaType":"image/jpeg",
  "downloadUrl":"/v1/runs/e2d8.../artifacts/a1b2..."
}
```

```http
GET /v1/runs/e2d8.../artifacts/a1b2...
Authorization: Bearer <token>
```

Artifacts expire according to the server retention setting. The client
verifies the announced size and SHA-256 before atomically saving the file.
