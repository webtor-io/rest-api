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
