//go:build oui_runtime_update

package pg_oui

import (
    "bufio"
    "bytes"
    "encoding/binary"
    "encoding/csv"
    "errors"
    "fmt"
    "io"
    "io/fs"
    "net/http"
    "os"
    "path/filepath"
    "regexp"
    "sort"
    "strconv"
    "strings"
    "time"
)

const ouiURL = "https://standards-oui.ieee.org/oui/oui.csv"

func resolveOrBuild(cfg *openCfg) (fs.FS, error) {
    if cfg.fsys != nil {
        if f, err := cfg.fsys.Open(cfg.entriesName); err == nil {
            f.Close()
            return cfg.fsys, nil
        }
    }
    dir := cfg.dir
    if dir == "" {
        if env := os.Getenv("PG_OUI_DATA_DIR"); env != "" {
            dir = env
        } else if cdir, err := os.UserCacheDir(); err == nil {
            dir = filepath.Join(cdir, "pg-oui")
        } else {
            dir = "."
        }
    }
    if exists(filepath.Join(dir, cfg.entriesName)) && exists(filepath.Join(dir, cfg.vendorsName)) && exists(filepath.Join(dir, cfg.indexName)) {
        return os.DirFS(dir), nil
    }
    if !cfg.autoUpdate {
        return nil, fmt.Errorf("dataset not found and auto-update disabled (dir=%s)", dir)
    }
    if err := os.MkdirAll(dir, 0o755); err != nil {
        return nil, fmt.Errorf("create data dir: %w", err)
    }
    cl := defaultClient(cfg)
    resp, err := cl.Get(ouiURL)
    if err != nil {
        return nil, fmt.Errorf("download OUI CSV: %w", err)
    }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("download OUI CSV: status %d", resp.StatusCode)
    }
    if err := buildFromCSV(resp.Body, dir, cfg.filter); err != nil {
        return nil, fmt.Errorf("build dataset: %w", err)
    }
    return os.DirFS(dir), nil
}

func defaultClient(cfg *openCfg) *http.Client {
    if cfg.httpClient != nil {
        if cl, ok := cfg.httpClient.(*http.Client); ok {
            return cl
        }
    }
    return &http.Client{Timeout: 30 * time.Second}
}

func exists(path string) bool {
    st, err := os.Stat(path)
    return err == nil && !st.IsDir()
}

var (
    llcRegex  = regexp.MustCompile(`(?i),?\s*(llc|ltd|limited|inc|incorporated)\.?$`)
    coRegex   = regexp.MustCompile(`(?i),?\s*(co|company|corp|corporation)\.?$`)
    gmbhRegex = regexp.MustCompile(`(?i),?\s*gmbh\.?$`)
)

func simplifyName(name string) string {
    b := []byte(name)
    b = llcRegex.ReplaceAll(b, []byte{})
    b = coRegex.ReplaceAll(b, []byte{})
    b = gmbhRegex.ReplaceAll(b, []byte{})
    return strings.TrimSpace(string(b))
}

type entry struct {
    OUI      string
    VendorID int
    Vendor   string
}

func buildFromCSV(r io.Reader, outdir string, filt *Filter) error {
    c := csv.NewReader(r)
    if _, err := c.Read(); err != nil { // header
        if !errors.Is(err, io.EOF) {
            return fmt.Errorf("read header: %w", err)
        }
        return fmt.Errorf("empty CSV")
    }
    vendorSet := map[string]struct{}{}
    ouiSet := map[string]struct{}{}
    if filt != nil {
        for _, v := range filt.VendorNames {
            v = simplifyName(strings.TrimSpace(v))
            if v != "" {
                vendorSet[v] = struct{}{}
            }
        }
        for _, o := range filt.OUIs {
            o = strings.ToLower(strings.TrimSpace(o))
            o = macCleaner.Replace(o)
            if len(o) >= 6 {
                ouiSet[o[:6]] = struct{}{}
            }
        }
    }
    allowVendor := func(v string) bool {
        if filt == nil {
            return true
        }
        sv := simplifyName(v)
        if len(vendorSet) > 0 {
            if _, ok := vendorSet[sv]; !ok {
                return false
            }
        }
        if filt.VendorRegex != nil && !filt.VendorRegex.MatchString(sv) {
            return false
        }
        return true
    }
    allowOUI := func(o string) bool {
        if filt == nil || len(ouiSet) == 0 {
            return true
        }
        _, ok := ouiSet[strings.ToLower(o)]
        return ok
    }
    ouiMap := make(map[string]string)
    vendorMap := make(map[string]int)
    var entries []entry
    var vendors []string
    for id := 0; ; {
        rec, err := c.Read()
        if errors.Is(err, io.EOF) {
            break
        }
        if err != nil {
            return fmt.Errorf("read row: %w", err)
        }
        if len(rec) < 3 {
            continue
        }
        o := strings.ToLower(strings.TrimSpace(rec[1]))
        if !allowOUI(o) {
            continue
        }
        v := strings.ReplaceAll(strings.TrimSpace(rec[2]), `"`, "")
        if !allowVendor(v) {
            continue
        }
        v = simplifyName(v)
        if prev, ok := ouiMap[o]; ok {
            _ = prev
            continue
        }
        ouiMap[o] = v
        if _, ok := vendorMap[v]; !ok {
            vendors = append(vendors, v)
            vendorMap[v] = id
            id++
        }
        entries = append(entries, entry{OUI: o, Vendor: v, VendorID: vendorMap[v]})
    }
    sort.Slice(entries, func(i, j int) bool {
        ai, _ := strconv.ParseInt(entries[i].OUI, 16, 64)
        aj, _ := strconv.ParseInt(entries[j].OUI, 16, 64)
        return ai < aj
    })
    if err := os.MkdirAll(outdir, 0o755); err != nil {
        return fmt.Errorf("mkdir outdir: %w", err)
    }
    // Write entries
    ef, err := os.Create(filepath.Join(outdir, defaultEntries))
    if err != nil {
        return fmt.Errorf("create entries: %w", err)
    }
    var buf bytes.Buffer
    for _, e := range entries {
        fmt.Fprintf(&buf, "%s,%d\n", e.OUI, e.VendorID)
    }
    if _, err := ef.Write(buf.Bytes()); err != nil {
        return fmt.Errorf("write entries: %w", err)
    }
    _ = ef.Close()
    // Write vendors
    vf, err := os.Create(filepath.Join(outdir, defaultVendors))
    if err != nil {
        return fmt.Errorf("create vendors: %w", err)
    }
    buf.Reset()
    for _, v := range vendors {
        fmt.Fprintf(&buf, "%s\n", v)
    }
    if _, err := vf.Write(buf.Bytes()); err != nil {
        return fmt.Errorf("write vendors: %w", err)
    }
    _ = vf.Close()
    // Create index
    if err := createIndex(filepath.Join(outdir, defaultVendors)); err != nil {
        return fmt.Errorf("create index: %w", err)
    }
    return nil
}

func createIndex(dataFile string) error {
    file, err := os.Open(dataFile)
    if err != nil {
        return fmt.Errorf("failed to open data file: %w", err)
    }
    defer file.Close()
    indexFile, err := os.Create(dataFile + ".index")
    if err != nil {
        return fmt.Errorf("failed to create index file: %w", err)
    }
    defer indexFile.Close()
    var off int64 = 0
    if err := binary.Write(indexFile, binary.LittleEndian, off); err != nil {
        return fmt.Errorf("write initial offset: %w", err)
    }
    scanner := bufio.NewScanner(file)
    for scanner.Scan() {
        off += int64(len(scanner.Bytes()) + 1)
        if err := binary.Write(indexFile, binary.LittleEndian, off); err != nil {
            return fmt.Errorf("write offset: %w", err)
        }
    }
    if err := scanner.Err(); err != nil {
        return fmt.Errorf("scan vendors: %w", err)
    }
    return nil
}

