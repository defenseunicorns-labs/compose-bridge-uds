package main

import (
	"errors"
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

	if err := run(input, output); err != nil {
		fail(err)
	}
}

func run(input, output string) error {
	conversion, conversionErr := compose.ConvertCanonicalFile(input)
	if conversionErr != nil {
		return errors.Join(conversionErr, render.WriteConversionReport(output, conversion.Report))
	}
	return render.WriteConversion(output, conversion)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
