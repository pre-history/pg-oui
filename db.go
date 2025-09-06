package pg_oui

import (
    "bytes"
    "encoding/binary"
    "encoding/csv"
    "errors"
    "fmt"
    "io"
    "io/fs"
    "net"
    "os"
    "strings"
    "sync"
)

// DB is an in-memory OUI database backed by files loaded from an fs.FS.
// It is safe for concurrent Lookups after Open completes.
type DB struct {
    entries map[string]int // OUI (lower, 6 chars) -> vendorID (1-based)
    vendors []byte         // full vendors file contents
    offsets []int64        // little-endian 64-bit offsets, length = lines+1
}

// Option configures Open.
type Option func(*openCfg)

type openCfg struct {
    fsys        fs.FS
    dir         string
    entriesName string
    vendorsName string
    indexName   string
    autoUpdate  bool
    cacheDir    string
    httpClient  any
    filter      *Filter
}

// WithFS sets the filesystem to load data files from.
func WithFS(fsys fs.FS) Option { return func(c *openCfg) { c.fsys = fsys } }

// WithDir uses a directory on disk as the data source.
func WithDir(path string) Option { return func(c *openCfg) { c.fsys = os.DirFS(path); c.dir = path } }

// WithFiles overrides the file names for entries/vendors/index within the fs.
func WithFiles(entries, vendors, index string) Option {
    return func(c *openCfg) {
        if entries != "" {
            c.entriesName = entries
        }
        if vendors != "" {
            c.vendorsName = vendors
        }
        if index != "" {
            c.indexName = index
        }
    }
}

const (
    defaultEntries = "entries"
    defaultVendors = "vendors"
    defaultIndex   = "vendors.index"
)

var macCleaner = strings.NewReplacer(":", "", "-", "", ".", "", " ", "")

// WithAutoUpdate enables (true) or disables (false) downloading/building the dataset
// when not present. Defaults to true.
func WithAutoUpdate(v bool) Option { return func(c *openCfg) { c.autoUpdate = v } }

// WithCacheDir sets the directory used when auto-updating to store generated files.
// If not set, it uses $PG_OUI_DATA_DIR or the user's cache dir (pg-oui) fallback.
func WithCacheDir(dir string) Option { return func(c *openCfg) { c.cacheDir = dir } }

// WithHTTPClient overrides the HTTP client used for downloading when built with the
// 'oui_runtime_update' build tag. In default builds, it has no effect.
func WithHTTPClient(cl any) Option { return func(c *openCfg) { c.httpClient = cl } }

// WithFilter applies a filter when generating the dataset during auto-update.
// It has no effect if data is already present.
func WithFilter(f *Filter) Option { return func(c *openCfg) { c.filter = f } }

// Open loads the OUI dataset from the provided fs and returns a DB.
func Open(opts ...Option) (*DB, error) {
    cfg := openCfg{
        fsys:        nil,
        entriesName: defaultEntries,
        vendorsName: defaultVendors,
        indexName:   defaultIndex,
        autoUpdate:  false,
    }
    for _, o := range opts {
        o(&cfg)
    }
    // Resolve filesystem or generate into cache dir if missing
    fsys, err := resolveOrBuild(&cfg)
    if err != nil {
        return nil, err
    }
    cfg.fsys = fsys
    // Load entries
    entriesFile, err := cfg.fsys.Open(cfg.entriesName)
    if err != nil {
        return nil, fmt.Errorf("open entries: %w", err)
    }
    defer entriesFile.Close()
    reader := csv.NewReader(entriesFile)
    entries := make(map[string]int, 4096)
    for {
        rec, err := reader.Read()
        if errors.Is(err, io.EOF) {
            break
        }
        if err != nil {
            return nil, fmt.Errorf("read entries: %w", err)
        }
        if len(rec) < 2 {
            continue
        }
        // vendorID must be numeric; ignore malformed
        id, err := atoi(rec[1])
        if err != nil {
            continue
        }
        entries[rec[0]] = id
    }

    // Load vendors file into memory
    vendorsBytes, err := fs.ReadFile(cfg.fsys, cfg.vendorsName)
    if err != nil {
        return nil, fmt.Errorf("read vendors: %w", err)
    }

    // Load index (sequence of little-endian int64 offsets)
    indexBytes, err := fs.ReadFile(cfg.fsys, cfg.indexName)
    if err != nil {
        return nil, fmt.Errorf("read index: %w", err)
    }
    r := bytes.NewReader(indexBytes)
    var offsets []int64
    for {
        var off int64
        if err := binary.Read(r, binary.LittleEndian, &off); err != nil {
            if errors.Is(err, io.EOF) {
                break
            }
            return nil, fmt.Errorf("parse index: %w", err)
        }
        offsets = append(offsets, off)
    }
    if len(offsets) == 0 {
        return nil, fmt.Errorf("index is empty")
    }

    return &DB{entries: entries, vendors: vendorsBytes, offsets: offsets}, nil
}

// Lookup returns the vendor name for the given MAC (or OUI) string.
// It returns ok=false when the OUI is unknown.
func (db *DB) Lookup(s string) (string, bool) {
    // Normalize
    s = macCleaner.Replace(s)
    if len(s) < 6 {
        return "", false
    }
    if len(s) > 6 {
        s = s[:6]
    }
    s = strings.ToLower(s)

    id, ok := db.entries[s]
    if !ok || id <= 0 {
        return "", false
    }
    v, err := db.vendorByID(id)
    if err != nil {
        return "", false
    }
    return v, true
}

// LookupFromHardwareAddr returns the vendor for a net.HardwareAddr.
func (db *DB) LookupFromHardwareAddr(hw net.HardwareAddr) (string, bool) {
    return db.Lookup(hw.String())
}

func (db *DB) vendorByID(id int) (string, error) {
    // ids are 1-based; offsets[i] is start of line i+1
    idx := id - 1
    if idx < 0 || idx+1 >= len(db.offsets) {
        return "", fmt.Errorf("id out of range")
    }
    start := db.offsets[idx]
    end := db.offsets[idx+1]
    if start < 0 || end < start || int(end) > len(db.vendors) {
        // Fallback: scan to newline
        if int(start) >= len(db.vendors) {
            return "", fmt.Errorf("offset out of range")
        }
        b := db.vendors[start:]
        if i := bytes.IndexByte(b, '\n'); i >= 0 {
            return string(bytes.TrimRight(b[:i], "\r\n")), nil
        }
        return string(bytes.TrimRight(b, "\r\n")), nil
    }
    line := db.vendors[start:end]
    // Offsets are written after each line including the newline
    line = bytes.TrimRight(line, "\r\n")
    return string(line), nil
}

// Default DB singleton and wrappers
var (
    defOnce sync.Once
    defDB   *DB
    defErr  error
)

func defaultDB() (*DB, error) {
    defOnce.Do(func() {
        // Allow overriding the data directory via env var
        if dir := os.Getenv("PG_OUI_DATA_DIR"); dir != "" {
            defDB, defErr = Open(WithDir(dir))
            return
        }
        defDB, defErr = Open() // current directory
    })
    return defDB, defErr
}

// Lookup is a package-level helper that uses a default DB.
func Lookup(s string) (string, bool) {
    db, err := defaultDB()
    if err != nil {
        return "", false
    }
    return db.Lookup(s)
}

// SearchVendor keeps backward compatibility by returning string only.
func SearchVendor(s string) string {
    v, _ := Lookup(s)
    return v
}

// SearchVendorFromMAC is a compatibility wrapper using the default DB.
func SearchVendorFromMAC(hw net.HardwareAddr) string {
    v, _ := Lookup(hw.String())
    return v
}

// Minimal atoi to avoid extra deps in tight environments.
func atoi(s string) (int, error) {
    n := 0
    if s == "" {
        return 0, fmt.Errorf("empty")
    }
    neg := false
    i := 0
    if s[0] == '-' {
        neg = true
        i = 1
        if len(s) == 1 {
            return 0, fmt.Errorf("invalid")
        }
    }
    for ; i < len(s); i++ {
        c := s[i]
        if c < '0' || c > '9' {
            return 0, fmt.Errorf("invalid")
        }
        n = n*10 + int(c-'0')
    }
    if neg {
        n = -n
    }
    return n, nil
}
