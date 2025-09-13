package main

import (
	"bufio"
	"encoding/binary"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	ouiURL = "https://standards-oui.ieee.org/oui/oui.csv"
)

func download() error {

	log.Printf("downloading %q", ouiURL)

	resp, err := http.Get(ouiURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: status %d", resp.StatusCode)
	}

	fout, err := os.Create("tmp_oui.csv")
	if err != nil {
		return err
	}
	defer fout.Close()

	_, err = io.Copy(fout, resp.Body)
	if err != nil {
		return err
	}

	return nil
}

type OUI string

type entry struct {
	OUI      OUI
	VendorID int
	Vendor   string
}
type templateData struct {
	Entries []entry
	Vendors []string
}

func (o OUI) String() string {
	return string(o)
}

func (o OUI) Int() int64 {
	n, err := strconv.ParseInt(o.String(), 16, 64)
	if err != nil {
		panic(err)
	}

	return n
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

	return string(b)
}

type filter struct {
	vendorSet   map[string]struct{} // simplified names
	vendorRegex *regexp.Regexp      // applied to simplified names
	ouiSet      map[string]struct{} // lower 6-hex
}

func (f *filter) allowVendor(name string) bool {
	if f == nil {
		return true
	}
	s := simplifyName(strings.TrimSpace(strings.ReplaceAll(name, `"`, "")))
	if len(f.vendorSet) > 0 {
		if _, ok := f.vendorSet[s]; !ok {
			return false
		}
	}
	if f.vendorRegex != nil && !f.vendorRegex.MatchString(s) {
		return false
	}
	return true
}

func (f *filter) allowOUI(o string) bool {
	if f == nil || len(f.ouiSet) == 0 {
		return true
	}
	_, ok := f.ouiSet[strings.ToLower(o)]
	return ok
}

func newTemplateData(r io.Reader, flt *filter) *templateData {

	var (
		entries []entry
		vendors []string
	)

	ouiMap := make(map[string]string)
	vendorMap := make(map[string]int)

	c := csv.NewReader(r)

	_, err := c.Read() // skip header
	if err != nil {
		panic(err)
	}

	for id := 0; ; {
		record, err := c.Read()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			panic(err)
		}

		o := strings.ToLower(record[1])

		if flt != nil && !flt.allowOUI(o) {
			continue
		}

		v := strings.TrimSpace(record[2])
		v = strings.ReplaceAll(v, `"`, "")
		v = simplifyName(v)

		if flt != nil && !flt.allowVendor(v) {
			continue
		}

		if prev, ok := ouiMap[o]; ok { // 080030 is a known duplicate
			log.Printf("Warning %q:%q is already registered to %q", o, v, prev)
			continue
		}

		ouiMap[o] = v

		if _, ok := vendorMap[v]; !ok {
			vendors = append(vendors, v)
			vendorMap[v] = id
			id++
		}

		entries = append(entries, entry{OUI: OUI(o), Vendor: v, VendorID: vendorMap[v]})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].OUI.Int() < entries[j].OUI.Int()
	})

	return &templateData{
		Entries: entries,
		Vendors: vendors,
	}
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

	writer := bufio.NewWriter(indexFile)
	defer writer.Flush()

	scanner := bufio.NewScanner(file)
	var offset int64 = 0

	// Write the offset of the first line (which is always 0)
	if err := binary.Write(writer, binary.LittleEndian, offset); err != nil {
		return fmt.Errorf("failed to write offset to index file: %w", err)
	}

	for scanner.Scan() {
		// The new offset is the previous offset plus the length of the line plus the newline character
		offset += int64(len(scanner.Bytes()) + 1)
		if err := binary.Write(writer, binary.LittleEndian, offset); err != nil {
			return fmt.Errorf("failed to write offset to index file: %w", err)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error while scanning data file: %w", err)
	}

	fmt.Println("Index file created successfully.")
	return nil
}

func getLine(dataFile string, lineNumber int) (string, error) {
	indexFileName := dataFile + ".index"
	indexFile, err := os.Open(indexFileName)
	if err != nil {
		return "", fmt.Errorf("failed to open index file. Please create it first using 'create-index': %w", err)
	}
	defer indexFile.Close()

	// Each offset is stored as a 64-bit integer (8 bytes)
	offsetPosition := int64(lineNumber-1) * 8
	if _, err := indexFile.Seek(offsetPosition, io.SeekStart); err != nil {
		return "", fmt.Errorf("failed to seek in index file: %w", err)
	}

	var offset int64
	if err := binary.Read(indexFile, binary.LittleEndian, &offset); err != nil {
		if err == io.EOF {
			return "", fmt.Errorf("line number %d is out of range", lineNumber)
		}
		return "", fmt.Errorf("failed to read offset from index file: %w", err)
	}

	dataFileHandle, err := os.Open(dataFile)
	if err != nil {
		return "", fmt.Errorf("failed to open data file: %w", err)
	}
	defer dataFileHandle.Close()

	if _, err := dataFileHandle.Seek(offset, io.SeekStart); err != nil {
		return "", fmt.Errorf("failed to seek in data file: %w", err)
	}

	reader := bufio.NewReader(dataFileHandle)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("failed to read line from data file: %w", err)
	}

	return line, nil
}

func updateData(outdir string, flt *filter) {
	file, err := os.Open("tmp_oui.csv")
	if err != nil {
		return
	}
	defer file.Close()

	data := newTemplateData(file, flt)

	if outdir == "" {
		outdir = "."
	}
	if err := os.MkdirAll(outdir, 0o755); err != nil {
		log.Printf("failed to create outdir %q: %v", outdir, err)
		return
	}

	fileEntries, err := os.Create(filepath.Join(outdir, "entries"))
	if err != nil {
		return
	}
	defer fileEntries.Close()

	w := bufio.NewWriter(fileEntries)
	for _, entryRow := range data.Entries {
		fmt.Fprintln(w, fmt.Sprintf("%s,%d", entryRow.OUI.String(), entryRow.VendorID))
	}
	_ = w.Flush()

	fileVendors, err := os.Create(filepath.Join(outdir, "vendors"))
	if err != nil {
		return
	}
	defer fileVendors.Close()

	w = bufio.NewWriter(fileVendors)
	for _, vendorRow := range data.Vendors {
		fmt.Fprintln(w, vendorRow)
	}
	_ = w.Flush()

	_ = createIndex(fileVendors.Name())

	os.Remove("tmp_oui.csv")

}
func readLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw := strings.Split(string(b), "\n")
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func parseFilter(includeVendors, includeVendorsFile, includeOUIs, includeOUIsFile, vendorRegex string) (*filter, error) {
	f := &filter{vendorSet: map[string]struct{}{}, ouiSet: map[string]struct{}{}}
	// vendor set (comma)
	if includeVendors != "" {
		for _, v := range strings.Split(includeVendors, ",") {
			v = simplifyName(strings.TrimSpace(v))
			if v != "" {
				f.vendorSet[v] = struct{}{}
			}
		}
	}
	// vendor set (file)
	if includeVendorsFile != "" {
		lines, err := readLines(includeVendorsFile)
		if err != nil {
			return nil, fmt.Errorf("read vendors file: %w", err)
		}
		for _, v := range lines {
			v = simplifyName(strings.TrimSpace(v))
			if v != "" {
				f.vendorSet[v] = struct{}{}
			}
		}
	}
	// vendor regex
	if vendorRegex != "" {
		rx, err := regexp.Compile(vendorRegex)
		if err != nil {
			return nil, fmt.Errorf("compile vendor-regex: %w", err)
		}
		f.vendorRegex = rx
	}
	// OUI set (comma)
	if includeOUIs != "" {
		for _, o := range strings.Split(includeOUIs, ",") {
			o = strings.ToLower(strings.TrimSpace(o))
			o = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(o, ":", ""), "-", ""), ".", "")
			if len(o) >= 6 {
				f.ouiSet[o[:6]] = struct{}{}
			}
		}
	}
	// OUI set (file)
	if includeOUIsFile != "" {
		lines, err := readLines(includeOUIsFile)
		if err != nil {
			return nil, fmt.Errorf("read ouis file: %w", err)
		}
		for _, o := range lines {
			o = strings.ToLower(strings.TrimSpace(o))
			o = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(o, ":", ""), "-", ""), ".", "")
			if len(o) >= 6 {
				f.ouiSet[o[:6]] = struct{}{}
			}
		}
	}
	// If both vendor and OUI filters empty and no regex, return nil to avoid filter cost
	if len(f.vendorSet) == 0 && len(f.ouiSet) == 0 && f.vendorRegex == nil {
		return nil, nil
	}
	return f, nil
}

func main() {
	outdir := flag.String("outdir", ".", "output directory for entries/vendors and index")
	incV := flag.String("include-vendors", "", "comma-separated list of vendor names to include (simplified)")
	incVFile := flag.String("include-vendors-file", "", "file with vendor names to include (one per line)")
	incO := flag.String("include-ouis", "", "comma-separated list of OUIs to include (e.g. 0CB4A4, 00:11:22)")
	incOFile := flag.String("include-ouis-file", "", "file with OUIs to include (one per line)")
	vRegex := flag.String("vendor-regex", "", "regex applied to simplified vendor names to include")
	skipDownload := flag.Bool("skip-download", false, "reuse existing tmp_oui.csv if present")
	flag.Parse()

	flt, err := parseFilter(*incV, *incVFile, *incO, *incOFile, *vRegex)
	if err != nil {
		log.Fatalf("filter error: %v", err)
	}

	if !*skipDownload {
		if err := download(); err != nil {
			log.Fatalf("download: %v", err)
		}
	}
	updateData(*outdir, flt)
}
