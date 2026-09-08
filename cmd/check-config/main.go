package main

import (
	"io"
	"os"

	"github.com/porsche/ai-gateway-go/internal/config"
)

func run(stdout, stderr io.Writer) int {
	if _, err := config.Load(); err != nil {
		if !writeLine(stderr, err.Error()) {
			return 1
		}
		return 1
	}
	if !writeLine(stdout, "configuration valid") {
		return 1
	}
	return 0
}

func writeLine(writer io.Writer, message string) bool {
	line := message + "\n"
	written, err := io.WriteString(writer, line)
	return err == nil && written == len(line)
}

func main() {
	os.Exit(run(os.Stdout, os.Stderr))
}
