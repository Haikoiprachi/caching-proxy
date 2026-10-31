# caching-proxy

A small CLI tool that starts a caching proxy server. It forwards requests to an origin server and caches the responses. If the same request is made again, the cached response is returned instead of hitting the origin.

## Build

```
go build -o caching-proxy .
```

## Usage

Start the proxy:

```
./caching-proxy --port 3000 --origin http://dummyjson.com
```

Now `http://localhost:3000/products` is forwarded to `http://dummyjson.com/products`.

Clear the cache of a running proxy:

```
./caching-proxy --port 3000 --clear-cache
```

### Flags

| Flag | Description | Default |
|---|---|---|
| `--port` | Port the proxy listens on | `3000` |
| `--origin` | URL of the origin server (required to start) | none |
| `--ttl` | Cache expiry, e.g. `30s`, `5m` (`0` = never expires) | `0` |
| `--clear-cache` | Clear the cache of the proxy running on `--port` | `false` |

## How to test

```
curl -i http://localhost:3000/products/1     # X-Cache: MISS
curl -i http://localhost:3000/products/1     # X-Cache: HIT
```

## How it works

- Only `GET` requests are cached.
- Only `200` responses are cached.
- Cache key is the method plus path plus query string (`GET /products?limit=10`).
- Every response carries an `X-Cache: HIT` or `X-Cache: MISS` header.
- The cache is in memory, so it is lost when the proxy stops.
