// Command gen writes the schema table of one midPoint line from its XSD files.
//
//	go run ./internal/mpschema/gen -line 4.9 -source <image> -xsd <dir> -out <file>
//
// `make schema-gen` extracts the XSDs from the images in
// internal/mpschema/images.txt and runs it for each line.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	line := flag.String("line", "", "midPoint line the schemas belong to, such as 4.9")
	source := flag.String("source", "", "image reference, with digest, the schemas came from")
	xsd := flag.String("xsd", "", "directory holding the extracted XSD files")
	out := flag.String("out", "", "table file to write")
	flag.Parse()
	if *line == "" || *source == "" || *xsd == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	b, err := generate(*xsd, *line, *source)
	if err == nil {
		err = os.WriteFile(*out, b, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}
