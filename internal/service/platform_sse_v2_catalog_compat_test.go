package service

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

const configuredModelsKey = "JIEKOU_ALLOWED_MODELS="

var documentedModelsExample = regexp.MustCompile("(?s)`JIEKOU_ALLOWED_MODELS` 使用逗号分隔的精确模型 ID，例如\\s*`([^`]+)`")

func TestPlatformSSEV2ConfiguredExampleModelsFitPersistenceColumns(t *testing.T) {
	envModels := configuredExampleModelIdentifiers(t, "../../.env.example")
	readmeModels := documentedExampleModelIdentifiers(t, "../../README.md")

	if strings.Join(envModels, "\x00") != strings.Join(readmeModels, "\x00") {
		t.Fatalf("README model example %q does not match .env.example %q", readmeModels, envModels)
	}
	if err := validatePlatformSSEV2ExampleModels(envModels); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformSSEV2ExampleModelCompatibilityRejectsOverlongFixture(t *testing.T) {
	if err := validatePlatformSSEV2ExampleModels([]string{strings.Repeat("m", 129)}); err == nil {
		t.Fatal("129-byte configured model fixture was accepted")
	}
}

func configuredExampleModelIdentifiers(t *testing.T, path string) []string {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	var assignments []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, configuredModelsKey) {
			assignments = append(assignments, strings.TrimPrefix(line, configuredModelsKey))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(assignments) != 1 {
		t.Fatalf("%s has %d active %s assignments, want exactly one", path, len(assignments), configuredModelsKey)
	}
	return parseExactExampleModelIdentifiers(t, path, assignments[0])
}

func documentedExampleModelIdentifiers(t *testing.T, path string) []string {
	t.Helper()

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	matches := documentedModelsExample.FindAllSubmatch(contents, -1)
	if len(matches) != 1 {
		t.Fatalf("%s has %d exact-model allowlist examples, want exactly one", path, len(matches))
	}
	return parseExactExampleModelIdentifiers(t, path, string(matches[0][1]))
}

func parseExactExampleModelIdentifiers(t *testing.T, source, value string) []string {
	t.Helper()

	parts := strings.Split(value, ",")
	models := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		model := strings.TrimSpace(part)
		if model == "" || strings.HasPrefix(model, "re:") {
			t.Fatalf("%s exact-model example contains an empty or pattern entry", source)
		}
		if _, exists := seen[model]; exists {
			t.Fatalf("%s exact-model example contains a duplicate entry", source)
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	return models
}

func validatePlatformSSEV2ExampleModels(models []string) error {
	if len(models) == 0 {
		return fmt.Errorf("configured model example must not be empty")
	}
	for index, model := range models {
		if !platformSSEV2ModelIdentifier(model) {
			return fmt.Errorf("configured model example at index %d is not valid UTF-8 within %d bytes", index, platformSSEV2MaxModelIdentifierBytes)
		}
	}
	return nil
}
