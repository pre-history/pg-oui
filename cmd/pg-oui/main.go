package main

import (
	"bufio"
	"flag"
	"fmt"
	pg_oui "github.com/pre-history/pg-oui"
	"io"
	"os"
)

func main() {
	dir := flag.String("dir", "", "data directory containing entries/vendors/vendors.index")
	flag.Parse()

	var opts []pg_oui.Option
	if *dir != "" {
		opts = append(opts, pg_oui.WithDir(*dir))
	}

	db, err := pg_oui.Open(opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(2)
	}

	args := flag.Args()
	if len(args) > 0 {
		for _, s := range args {
			if v, ok := db.Lookup(s); ok {
				fmt.Println(v)
			} else {
				fmt.Println("")
			}
		}
		return
	}

	// Read from stdin, one per line
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		fmt.Fprintln(os.Stderr, "usage: pg-oui [-dir path] <MAC-or-OUI> [...] | echo <MAC> | pg-oui [-dir path]")
		os.Exit(1)
	}

	r := bufio.NewReader(os.Stdin)
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			if v, ok := db.Lookup(line); ok {
				fmt.Println(v)
			} else {
				fmt.Println("")
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "read stdin: %v\n", err)
			os.Exit(2)
		}
	}
}
