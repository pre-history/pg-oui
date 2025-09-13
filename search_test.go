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

	// vendor IDs map to offset indices (0-based relative to lines)
	writeVendors(t, dir, []string{"Vendor One", "Vendor Two"})
	writeEntries(t, dir, map[string]int{
		"abcdef": 0,
		"abcd12": 1,
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

func TestOUIIndexing(t *testing.T) {
	dir := t.TempDir()

	// Regression test for off-by-one indexing bug.
	// The entries file stores vendor IDs that should map to line numbers in the vendors file.
	// Previously, there was an off-by-one error where ID N would return line N+1.

	vendors := []string{
		"First Vendor",  // line 1, should be ID 1
		"Second Vendor", // line 2, should be ID 2
		"Third Vendor",  // line 3, should be ID 3
		"Fourth Vendor", // line 4, should be ID 4
		"Fifth Vendor",  // line 5, should be ID 5
	}
	writeVendors(t, dir, vendors)

	entries := map[string]int{
		"000001": 1, // Should map to "Second Vendor" (offsets[1] to offsets[2])
		"000002": 2, // Should map to "Third Vendor" (offsets[2] to offsets[3])
		"000003": 3, // Should map to "Fourth Vendor" (offsets[3] to offsets[4])
		"000004": 4, // Should map to "Fifth Vendor" (offsets[4] to offsets[5])
	}
	writeEntries(t, dir, entries)

	db, err := Open(WithDir(dir), WithAutoUpdate(false))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Test each mapping to ensure vendor IDs correctly map to line numbers
	testCases := []struct {
		oui      string
		expected string
		id       int
	}{
		{"000001", "Second Vendor", 1},
		{"000002", "Third Vendor", 2},
		{"000003", "Fourth Vendor", 3},
		{"000004", "Fifth Vendor", 4},
	}

	for _, tc := range testCases {
		got, ok := db.Lookup(tc.oui)
		if !ok {
			t.Errorf("OUI %s (ID %d): not found, expected %q", tc.oui, tc.id, tc.expected)
			continue
		}
		if got != tc.expected {
			t.Errorf("OUI %s (ID %d): got %q, expected %q", tc.oui, tc.id, got, tc.expected)
		}
	}
}

func TestOUIIndexingRealData(t *testing.T) {
	// Test against real data to ensure known OUI mappings work correctly
	db, err := Open(WithAutoUpdate(false))
	if err != nil {
		t.Skipf("skipping real data test: %v", err)
	}

	// These are real OUI mappings that were failing due to the off-by-one bug
	testCases := []struct {
		oui      string
		expected string
	}{
		{"08d1f9", "Espressif"},               // ID 14 -> line 14
		{"f42679", "Intel Corporate"},         // ID 25 -> line 25
		{"b827eb", "Raspberry Pi Foundation"}, // ID 17003 -> line 17003
		{"3c8d20", "Google"},                  // ID 203 -> line 203
		{"704d7b", "ASUSTek COMPUTER"},        // ID 388 -> line 388
	}

	for _, tc := range testCases {
		got, ok := db.Lookup(tc.oui)
		if !ok {
			t.Errorf("OUI %s: not found, expected %q", tc.oui, tc.expected)
			continue
		}
		if got != tc.expected {
			t.Errorf("OUI %s: got %q, expected %q", tc.oui, got, tc.expected)
		}
	}
}
