pg-oui

Small Go library (with a debug CLI) to map a MAC OUI (first 3 bytes) to a vendor name, using pre-generated data files.

API
- Core
  - `db, _ := pg_oui.Open()` loads from the current directory’s data files.
  - `vendor, ok := db.Lookup(mac)` returns the vendor for a MAC/OUI.
  - `Lookup(mac)` and `SearchVendor(mac)` are package-level helpers using a default DB.
- Options
  - `pg_oui.WithDir(path)` loads from a specific directory.
  - `pg_oui.WithFS(fsys fs.FS)` loads from any filesystem (e.g., your own `embed.FS`).
  - `pg_oui.WithFiles(entries, vendors, index)` overrides file names.
- Example

  package main

  import (
      "fmt"
      pg_oui "github.com/pre-history/pg-oui"
  )

  func main() {
      db, _ := pg_oui.Open(pg_oui.WithDir("/opt/oui"))
      if v, ok := db.Lookup("0C-B4-A4-01-02-03"); ok {
          fmt.Println(v)
      }
  }

- Embedding a reduced dataset with `embed.FS`

  //go:embed data-nokia-sony/*
  var data embed.FS

  db, _ := pg_oui.Open(
      pg_oui.WithFS(data),
      pg_oui.WithFiles("data-nokia-sony/entries", "data-nokia-sony/vendors", "data-nokia-sony/vendors.index"),
  )

Data Files
- `entries`: CSV with `OUI,VendorID` per line (OUI is 6 lowercase hex).
- `vendors`: newline-delimited vendor names; 1-based line number equals `VendorID`.
- `vendors.index`: binary index of little-endian int64 offsets; length is lines+1.

Behavior
- Inputs are normalized: `:`, `-`, `.`, and spaces are stripped; only the first 6 hex characters are used; case-insensitive.
- Lookups avoid per-call CSV scans: `entries` is held in memory; vendor strings are read via offsets; results are trimmed of trailing newlines.
- `Lookup` returns `(string, bool)`; `SearchVendor` returns `string` for backward compatibility.
- Default DB (no runtime downloads):
  - The library does not fetch data at runtime. Provide data via a directory (`WithDir`) or embed it via `WithFS`.
  - By convention, if `PG_OUI_DATA_DIR` is set, the default DB will read from that directory; otherwise, it looks in the current directory.

Filtering (compile smaller datasets)
- The updater supports allowlisting vendors and/or OUIs to generate a reduced dataset for embedding in your binaries:

  go build ./cmd/update_data
  ./update_data \
    -outdir ./data-nokia-sony \
    -include-vendors "Nokia,Sony" \
    -vendor-regex "(?i)^(Nokia|Sony)" \
    -include-ouis "0CB4A4, 001122"

- Flags
  - `-outdir`: where to write the new `entries`, `vendors`, and `vendors.index`.
  - `-include-vendors`: comma-separated vendor names (matched after simplification like LLC/Ltd/Inc removal).
  - `-include-vendors-file`: file with vendor names, one per line.
  - `-vendor-regex`: regex applied to simplified vendor names.
  - `-include-ouis`: comma-separated OUIs (any separator allowed; first 6 hex characters are used).
  - `-include-ouis-file`: file with OUIs, one per line.

CLI
- Debug helpers:

  go run ./cmd/pg-oui -dir . 0C-B4-A4-01-02-03
  echo 0C-B4-A4-01-02-03 | go run ./cmd/pg-oui -dir .
  go run ./cmd/fetch_mac_vendor

Updating Data (build-time only)
- Refresh from IEEE and rebuild indices into a target directory, then embed or ship those files:

  go build ./cmd/update_data && ./update_data -outdir ./data

Optional (dev only)
- A runtime auto-update mode exists behind a build tag for development convenience:

  go build -tags oui_runtime_update ./...

- In that mode, the library will download and cache the dataset if it’s missing. Do not enable this in production/router firmware.

License
- This repository’s license should match the terms of the IEEE OUI database you redistribute. Please ensure compliance with IEEE’s terms when generating and embedding datasets. If you provide the exact license text/terms to apply, we can add them here.
