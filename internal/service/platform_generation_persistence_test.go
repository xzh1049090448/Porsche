package service

import (
	"errors"
	"strings"
	"testing"
)

func validPlatformGenerationPersistenceInput() PlatformGenerationPersistenceInput {
	return PlatformGenerationPersistenceInput{
		UserID:       1,
		GenerationID: generationTestID,
		Mode:         PlatformGenerationModeSingle,
		Models:       []string{"model-a"},
		UserMessage:  " keep whitespace byte-for-byte ",
		Results: []PlatformGenerationPersistenceResult{{
			Model: "model-a", State: PlatformGenerationStateCompleted, Content: "answer", Tokens: 1,
		}},
		NowMillis: 1,
	}
}

func TestValidatePlatformGenerationPersistenceInputRejectsInvalidBeforeDependencies(t *testing.T) {
	unsafeTime := platformSSEV2MaxSafeInteger + 1
	invalidConversationGUID := int64(0)
	tests := []struct {
		name   string
		mutate func(*PlatformGenerationPersistenceInput)
	}{
		{"zero user", func(input *PlatformGenerationPersistenceInput) { input.UserID = 0 }},
		{"malformed UUID", func(input *PlatformGenerationPersistenceInput) { input.GenerationID = "not-a-uuid" }},
		{"nonpositive time", func(input *PlatformGenerationPersistenceInput) { input.NowMillis = 0 }},
		{"unsafe time", func(input *PlatformGenerationPersistenceInput) { input.NowMillis = unsafeTime }},
		{"invalid mode", func(input *PlatformGenerationPersistenceInput) { input.Mode = 99 }},
		{"single cardinality", func(input *PlatformGenerationPersistenceInput) {
			input.Models = []string{"model-a", "model-b"}
			input.Results = append(input.Results, PlatformGenerationPersistenceResult{Model: "model-b", State: PlatformGenerationStateFailed, ErrorCode: "timeout"})
		}},
		{"compare cardinality", func(input *PlatformGenerationPersistenceInput) { input.Mode = PlatformGenerationModeCompare }},
		{"duplicate model", func(input *PlatformGenerationPersistenceInput) {
			input.Mode = PlatformGenerationModeCompare
			input.Models = []string{"model-a", "model-a"}
			input.Results = []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "one", Tokens: 1},
				{Model: "model-a", State: PlatformGenerationStateFailed, ErrorCode: "timeout"},
			}
		}},
		{"oversized model", func(input *PlatformGenerationPersistenceInput) {
			input.Models[0] = strings.Repeat("m", 129)
			input.Results[0].Model = input.Models[0]
		}},
		{"missing result", func(input *PlatformGenerationPersistenceInput) { input.Results = nil }},
		{"extra result", func(input *PlatformGenerationPersistenceInput) {
			input.Results = append(input.Results, input.Results[0])
		}},
		{"model order mismatch", func(input *PlatformGenerationPersistenceInput) { input.Results[0].Model = "model-b" }},
		{"running result", func(input *PlatformGenerationPersistenceInput) {
			input.Results[0].State = PlatformGenerationStateRunning
		}},
		{"cancelling result", func(input *PlatformGenerationPersistenceInput) {
			input.Results[0].State = PlatformGenerationStateCancelling
		}},
		{"cancelled result", func(input *PlatformGenerationPersistenceInput) {
			input.Results[0].State = PlatformGenerationStateCancelled
		}},
		{"single failure", func(input *PlatformGenerationPersistenceInput) {
			input.Results[0] = PlatformGenerationPersistenceResult{Model: "model-a", State: PlatformGenerationStateFailed, ErrorCode: "timeout"}
		}},
		{"compare all failures", func(input *PlatformGenerationPersistenceInput) {
			input.Mode = PlatformGenerationModeCompare
			input.Models = []string{"model-a", "model-b"}
			input.Results = []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateFailed, ErrorCode: "timeout"},
				{Model: "model-b", State: PlatformGenerationStateFailed, ErrorCode: "upstream_error"},
			}
		}},
		{"completed empty content", func(input *PlatformGenerationPersistenceInput) { input.Results[0].Content = "" }},
		{"completed oversized content", func(input *PlatformGenerationPersistenceInput) { input.Results[0].Content = strings.Repeat("x", 65536) }},
		{"completed negative tokens", func(input *PlatformGenerationPersistenceInput) { input.Results[0].Tokens = -1 }},
		{"completed excessive tokens", func(input *PlatformGenerationPersistenceInput) { input.Results[0].Tokens = 1 << 31 }},
		{"completed error code", func(input *PlatformGenerationPersistenceInput) { input.Results[0].ErrorCode = "timeout" }},
		{"failed content", func(input *PlatformGenerationPersistenceInput) {
			input.Mode = PlatformGenerationModeCompare
			input.Models = []string{"model-a", "model-b"}
			input.Results = []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "one", Tokens: 1},
				{Model: "model-b", State: PlatformGenerationStateFailed, Content: "partial", ErrorCode: "timeout"},
			}
		}},
		{"failed tokens", func(input *PlatformGenerationPersistenceInput) {
			input.Mode = PlatformGenerationModeCompare
			input.Models = []string{"model-a", "model-b"}
			input.Results = []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "one", Tokens: 1},
				{Model: "model-b", State: PlatformGenerationStateFailed, Tokens: 1, ErrorCode: "timeout"},
			}
		}},
		{"failed missing code", func(input *PlatformGenerationPersistenceInput) {
			input.Mode = PlatformGenerationModeCompare
			input.Models = []string{"model-a", "model-b"}
			input.Results = []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "one", Tokens: 1},
				{Model: "model-b", State: PlatformGenerationStateFailed},
			}
		}},
		{"failed unstable code", func(input *PlatformGenerationPersistenceInput) {
			input.Mode = PlatformGenerationModeCompare
			input.Models = []string{"model-a", "model-b"}
			input.Results = []PlatformGenerationPersistenceResult{
				{Model: "model-a", State: PlatformGenerationStateCompleted, Content: "one", Tokens: 1},
				{Model: "model-b", State: PlatformGenerationStateFailed, ErrorCode: "provider_secret"},
			}
		}},
		{"empty user message", func(input *PlatformGenerationPersistenceInput) { input.UserMessage = "" }},
		{"oversized user message", func(input *PlatformGenerationPersistenceInput) { input.UserMessage = strings.Repeat("x", 65536) }},
		{"invalid conversation GUID", func(input *PlatformGenerationPersistenceInput) { input.ConversationGUID = &invalidConversationGUID }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validPlatformGenerationPersistenceInput()
			test.mutate(&input)
			if err := validatePlatformGenerationPersistenceInput(input); !errors.Is(err, ErrPlatformGenerationPersistenceInvalid) {
				t.Fatalf("error=%v, want typed invalid", err)
			}
		})
	}
}

func TestValidatePlatformGenerationPersistenceInputPreservesMessageBytes(t *testing.T) {
	input := validPlatformGenerationPersistenceInput()
	if err := validatePlatformGenerationPersistenceInput(input); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
}
