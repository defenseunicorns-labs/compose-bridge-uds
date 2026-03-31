package main

import (
	"flag"
	"fmt"
	"os"

	"defenseunicorns/uds-compose-bridge/internal/compose"
	"defenseunicorns/uds-compose-bridge/internal/render"
)

func main() {
	var input string
	var output string

	flag.StringVar(&input, "in", "/in/compose.yaml", "Path to the canonical Compose model")
	flag.StringVar(&output, "out", "/out", "Output directory for the generated UDS package")
	flag.Parse()

	app, err := compose.LoadCanonicalFile(input)
	if err != nil {
		fail(err)
	}

	if err := render.WritePackage(output, app); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
