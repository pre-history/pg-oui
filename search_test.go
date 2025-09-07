package pg_oui

import (
    "bufio"
    "encoding/binary"
    "os"
    "path/filepath"
    "testing"
)

func writeVendors(t *testing.T, dir string, lines []string) {
    t.Helper()
    vf, err := os.Create(filepath.Join(dir, "vendors"))
    if err != nil {
        t.Fatalf("create vendors: %v", err)
    }
    w := bufio.NewWriter(vf)
    for _, l := range lines {
        if _, err := w.WriteString(l + "\n"); err != nil {
            t.Fatalf("write vendors: %v", err)
        }
    }
    if err := w.Flush(); err != nil {
        t.Fatalf("flush vendors: %v", err)
    }
    _ = vf.Close()

    // Build index: write offset 0, then cumulative offsets per line
    idx, err := os.Create(filepath.Join(dir, "vendors.index"))
    if err != nil {
        t.Fatalf("create index: %v", err)
    }
    var off int64 = 0
    if err := binary.Write(idx, binary.LittleEndian, off); err != nil {
        t.Fatalf("write index: %v", err)
    }
    for _, l := range lines {
        off += int64(len(l) + 1) // include newline
        if err := binary.Write(idx, binary.LittleEndian, off); err != nil {
            t.Fatalf("write index: %v", err)
        }
    }
    _ = idx.Close()
}

func writeEntries(t *testing.T, dir string, pairs map[string]int) {
    t.Helper()
    ef, err := os.Create(filepath.Join(dir, "entries"))
    if err != nil {
        t.Fatalf("create entries: %v", err)
    }
    w := bufio.NewWriter(ef)
    for k, v := range pairs {
        if _, err := w.WriteString(k + "," + itoa(v) + "\n"); err != nil {
            t.Fatalf("write entries: %v", err)
        }
    }
    if err := w.Flush(); err != nil {
        t.Fatalf("flush entries: %v", err)
    }
    _ = ef.Close()
}

// minimal int -> string to avoid importing strconv in tests
func itoa(n int) string {
    if n == 0 {
        return "0"
    }
    neg := n < 0
    if neg {
        n = -n
    }
    // Build digits in reverse into a fixed buffer
    var buf [20]byte
    i := len(buf)
    for n > 0 {
        i--
        buf[i] = byte('0' + n%10)
        n /= 10
    }
    if neg {
        i--
        buf[i] = '-'
    }
    return string(buf[i:])
}

func TestSearchVendor_NormalizationAndTrim(t *testing.T) {
    dir := t.TempDir()

    // vendor IDs are 1-based line numbers
    writeVendors(t, dir, []string{"Vendor One", "Vendor Two"})
    writeEntries(t, dir, map[string]int{
        "abcdef": 1,
        "abcd12": 2,
    })

    db, err := Open(WithDir(dir), WithAutoUpdate(false))
    if err != nil {
        t.Fatalf("open: %v", err)
    }

    got, ok := db.Lookup("AB-CD-EF-12-34-56")
    if !ok || got != "Vendor One" {
        t.Fatalf("want 'Vendor One', got %q, ok=%v", got, ok)
    }

    got, ok = db.Lookup("ab.cd.ef")
    if !ok || got != "Vendor One" {
        t.Fatalf("want 'Vendor One', got %q, ok=%v", got, ok)
    }

    got, ok = db.Lookup("ab cd ef 99")
    if !ok || got != "Vendor One" {
        t.Fatalf("want 'Vendor One', got %q, ok=%v", got, ok)
    }

    got, ok = db.Lookup("abcd12deadbeef") // truncates to abcd12
    if !ok || got != "Vendor Two" {
        t.Fatalf("want 'Vendor Two', got %q, ok=%v", got, ok)
    }

    // Not found should return empty string / ok=false
    if v, ok := db.Lookup("123456"); ok || v != "" {
        t.Fatalf("want empty+false for not found, got %q ok=%v", v, ok)
    }
}
