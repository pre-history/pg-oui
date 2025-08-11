package pg_oui

import (
	"bufio"
	"encoding/binary"
	"encoding/csv"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
)

func SearchVendor(s string) string {
	s = strings.ReplaceAll(s, ":", "")

	switch {
	case len(s) < 6:
		return ""
	case len(s) > 6:
		s = s[0:6]
	}

	s = strings.ToLower(s)

	file, err := os.Open("entries")
	if err != nil {
		return ""
	}
	defer file.Close()

	reader := csv.NewReader(file)
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ""
		}

		if record[0] == s {
			lineNumber, _ := strconv.Atoi(record[1])
			lineRow, _ := getLine(lineNumber)
			return lineRow
		}
	}

	return ""
}

func getLine(lineNumber int) (string, error) {
	indexFileName := "vendors" + ".index"
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

	dataFileHandle, err := os.Open("vendors")
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

func SearchVendorFromMAC(hw net.HardwareAddr) string {
	return SearchVendor(hw.String())
}
