// Command loggen writes the deterministic synthetic incident dataset.
package main

import (
	"flag"
	"fmt"
	"log"

	"log-investigator/internal/loggen"
)

func main() {
	out := flag.String("out", "data", "output directory")
	flag.Parse()
	if err := loggen.Write(*out); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote synthetic logs to %s/\n", *out)
}
