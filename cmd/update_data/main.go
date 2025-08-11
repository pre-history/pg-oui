package main

import (
	"bufio"
	"encoding/binary"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
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

	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed: %w", err)
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

func newTemplateData(r io.Reader) *templateData {

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

		v := strings.TrimSpace(record[2])
		v = strings.ReplaceAll(v, `"`, "")
		v = simplifyName(v)

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

func createIndex() error {
	file, err := os.Open("vendors")
	if err != nil {
		return fmt.Errorf("failed to open data file: %w", err)
	}
	defer file.Close()

	indexFile, err := os.Create("vendors" + ".index")
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

func updateData() {
	file, err := os.Open("tmp_oui.csv")
	if err != nil {
		return
	}
	defer file.Close()

	data := newTemplateData(file)

	fileEntries, err := os.Create("entries")
	if err != nil {
		return
	}
	defer fileEntries.Close()

	w := bufio.NewWriter(fileEntries)
	for _, entryRow := range data.Entries {
		fmt.Fprintln(w, fmt.Sprintf("%s,%d", entryRow.OUI.String(), entryRow.VendorID))
	}
	_ = w.Flush()

	fileVendors, err := os.Create("vendors")
	if err != nil {
		return
	}
	defer fileVendors.Close()

	w = bufio.NewWriter(fileVendors)
	for _, vendorRow := range data.Vendors {
		fmt.Fprintln(w, vendorRow)
	}
	_ = w.Flush()

	_ = createIndex()

	os.Remove("tmp_oui.csv")

}
func main() {
	_ = download()

	updateData()

}
