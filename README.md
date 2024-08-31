![GitHub Repo stars](https://img.shields.io/github/stars/kory33/wlx212-gui-scraping-exporter?style=social)
![GitHub](https://img.shields.io/github/license/kory33/wlx212-gui-scraping-exporter)
![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/kory33/wlx212-gui-scraping-exporter)
![GitHub all releases](https://img.shields.io/github/downloads/kory33/wlx212-gui-scraping-exporter/total)
![GitHub CI Status](https://img.shields.io/github/actions/workflow/status/kory33/wlx212-gui-scraping-exporter/ci.yaml?branch=main&label=CI)
![GitHub Release Status](https://img.shields.io/github/v/release/kory33/wlx212-gui-scraping-exporter)

# wlx212-gui-scraping-exporter

Example response on `/metrics`

```
ap_connections{hostname="ap-01"} 10
ap_connections{hostname="ap-02"} 13
ap_connections{hostname="ap-03"} 12
```

Example response on `/aplist`

```
[{"hostname":"ap-01","active_connections":10},{"hostname":"ap-02","active_connections":13},{"hostname":"ap-03","active_connections":12}]
```

## Running the server

The server takes no command-line argument and all parameters are controlled by one of the following environment variables:

- Required:
  - `VIRTUAL_CONTROLLER_VIP` - the virtual IP address of the virtual controller
  - `VIRTUAL_CONTROLLER_GUI_USER` + `VIRTUAL_CONTROLLER_GUI_PASS` - login credential for accessing GUI of the virtual controller
- Optional:
  - `PORT` - the port to which the exporter server should be bound

## Build

```
go build -ldflags="-w -s -extldflags '-static'" -o=dist/wlx212-gui-scraping-exporter
```

## Author
kory33
