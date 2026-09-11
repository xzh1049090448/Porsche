package openaicompat

import (
	"strings"
	"unicode/utf8"
)

func validateConversation(c Conversation) *Error {
	if strings.TrimSpace(c.Model) == "" || len(c.Messages) > MaxMessages || len(c.Tools) > MaxTools {
		return InvalidRequest()
	}
	declared := make(map[string]struct{})
	open := make(map[string]struct{})
	for _, message := range append(append([]Message(nil), c.Instructions...), c.Messages...) {
		if message.Role != RoleTool && len(open) != 0 {
			return InvalidRequest()
		}
		switch message.Role {
		case RoleSystem, RoleDeveloper, RoleUser:
			if message.Content == nil || len(message.ToolCalls) != 0 || message.ToolCallID != "" {
				return InvalidRequest()
			}
		case RoleAssistant:
			if message.Content == nil && len(message.ToolCalls) == 0 || len(message.ToolCalls) > MaxParallelCalls || message.ToolCallID != "" {
				return InvalidRequest()
			}
			for _, call := range message.ToolCalls {
				if !validCallID(call.ID) || !validFunctionName(call.Name) || !utf8.ValidString(call.Arguments) || len(call.Arguments) > MaxArgumentsBytes {
					return InvalidRequest()
				}
				if _, exists := declared[call.ID]; exists {
					return InvalidRequest()
				}
				declared[call.ID] = struct{}{}
				open[call.ID] = struct{}{}
			}
		case RoleTool:
			if len(message.ToolCalls) != 0 || message.Content == nil {
				return InvalidRequest()
			}
			if _, exists := open[message.ToolCallID]; !exists {
				return InvalidRequest()
			}
			delete(open, message.ToolCallID)
		default:
			return InvalidRequest()
		}
	}
	if len(open) != 0 {
		return InvalidRequest()
	}
	return nil
}

func validCallID(id string) bool {
	if id == "" || len(id) > MaxCallIDBytes || !utf8.ValidString(id) {
		return false
	}
	for _, char := range id {
		if char < 0x21 || char > 0x7e {
			return false
		}
	}
	return true
}
