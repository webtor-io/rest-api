# rest-api

REST-API for Webtor can:
1. Store resource to Webtor (torrent/magnet-uri)
2. List content of stored resource
3. Export urls to content for downloading and streaming

## Basic usage

```
% ./rest-api help serve

NAME:
   rest-api serve - Serves web server

USAGE:
   rest-api serve [command options] [arguments...]

OPTIONS:
   --probe-host value                probe listening host
   --probe-port value                probe listening port (default: 8081)
   --host value                      listening host [$WEB_HOST]
   --port value                      http listening port (default: 8080) [$WEB_PORT]
   --torrent-store-host value        torrent store host [$TORRENT_STORE_SERVICE_HOST, $ TORRENT_STORE_HOST]
   --torrent-store-port value        torrent store port (default: 50051) [$TORRENT_STORE_SERVICE_PORT, $ TORRENT_STORE_PORT]
   --magnet2torrent-host value       magnet2torrent host [$MAGNET2TORRENT_SERVICE_HOST, $ MAGNET2TORRENT_HOST]
   --magnet2torrent-port value       magnet2torrent port (default: 50051) [$MAGNET2TORRENT_SERVICE_PORT, $ MAGNET2TORRENT_PORT]
   --export-domain value             export domain [$EXPORT_DOMAIN]
   --export-ssl                      export ssl [$EXPORT_SSL]
   --node-label-prefix value         node label prefix (default: "webtor.io/") [$NODE_LABEL_PREFIX]
   --node-iface value                node iface (default: "eth0") [$NODE_IFACE]
   --prom-addr value                 prometheus connection address [$PROM_ADDR]
```

## Swagger (OpenAPI)

http://localhost:8080/swagger/index.html

## Running the REST API Locally

Follow these steps to set up and run the Webtor REST API on your machine.

### 1. Prerequisites
Make sure you have the following installed:
- [Git](https://git-scm.com/)
- [Go](https://golang.org/) (if building from source)
- [Docker](https://www.docker.com/) (optional, for running dependencies)
- Network access to required ports (default: 8080 and 50051)

---

### 2. Clone the Repository

```bash
git clone https://github.com/webtor/rest-api.git
cd rest-api

---

### 3. Build or Download the API Binary

If building from source:

```bash
go build -o rest-api
```

---

### 4. Run Required Services

The API depends on two services: **torrent-store** and **magnet2torrent**. You can run them using Docker:

```bash
docker run -d --name torrent-store -p 50051:50051 webtor/torrent-store
docker run -d --name magnet2torrent -p 50051:50051 webtor/magnet2torrent
```

---

### 5. Set Environment Variables

You can create a `.env` file or export them in your shell:

```bash
export WEB_HOST=0.0.0.0
export WEB_PORT=8080

export TORRENT_STORE_HOST=localhost
export TORRENT_STORE_PORT=50051

export MAGNET2TORRENT_HOST=localhost
export MAGNET2TORRENT_PORT=50051

export EXPORT_DOMAIN=localhost
export EXPORT_SSL=false
```

These can also be passed as command-line options when starting the server.

---

### 6. Start the Server

Run the API server using the following command:

```bash
./rest-api serve
```

Or with explicit options:

```bash
./rest-api serve --host 0.0.0.0 --port 8080
```

---

### 7. Access API Documentation

Once the server is running, open the Swagger UI in your browser to explore the endpoints:

```
http://localhost:8080/swagger/index.html
```

---

### 8. Test the API

Here’s an example request to store a magnet link:

```bash
curl -X POST "http://localhost:8080/api/store" \
     -H "Content-Type: application/json" \
     -d '{"magnet": "magnet:?xt=urn:btih:..." }'
```

Verify the response to ensure the resource is stored correctly.

With these steps, you should be able to set up, run, and explore the Webtor REST API locally for development or testing purposes.

```

