package main

import (
	"fmt"
	"io"
	"os"

	"github.com/porsche/ai-gateway-go/internal/config"
)

func run(stdout, stderr io.Writer) int {
	if _, err := config.Load(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "configuration valid")
	return 0
}

func main() {
	os.Exit(run(os.Stdout, os.Stderr))
}
