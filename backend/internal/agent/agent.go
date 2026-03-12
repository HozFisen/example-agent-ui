package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"ui-agent/internal/mcp"
	"ui-agent/internal/models"
	"ui-agent/pkg/llm"
)

// Agent orchestrates the full agentic loop:
// user prompt → LLM reasoning → tool calls → structured instructions
type Agent struct {
	llm *llm.AnthropicClient
	mcp *mcp.Server
}

func NewAgent(llmClient *llm.AnthropicClient, mcpServer *mcp.Server) *Agent {
	return &Agent{llm: llmClient, mcp: mcpServer}
}

// Run executes the agent pipeline for a given user prompt.
// Returns a fully-populated TaskResponse ready to send to the frontend.
func (a *Agent) Run(req models.TaskRequest) (*models.TaskResponse, error) {
	taskID := generateID()

	// Build the enriched prompt that includes any file hint
	prompt := req.Prompt
	if req.UIFileID != "" {
		prompt = fmt.Sprintf("[Focus on UI file: %s]\n\n%s", req.UIFileID, req.Prompt)
	}

	// Run the agentic loop — LLM calls tools autonomously until done
	steps, tokens, err := a.llm.Complete(
		prompt,
		a.mcp.Tools(),
		a.mcp.Execute, // tool executor injected into the LLM loop
	)
	if err != nil {
		return nil, fmt.Errorf("agent loop failed: %w", err)
	}

	// Extract structured instructions from the final "thought" step
	instructions := extractInstructions(steps)

	// Add a final summary step if we successfully parsed instructions
	if len(instructions) > 0 {
		steps = append(steps, models.TaskStep{
			StepNumber:  len(steps) + 1,
			Type:        "instruction",
			Title:       fmt.Sprintf("Generated %d instructions", len(instructions)),
			Description: "Task analysis complete. Structured instructions ready.",
		})
	}

	return &models.TaskResponse{
		TaskID:            taskID,
		OriginalPrompt:    req.Prompt,
		Steps:             steps,
		FinalInstructions: instructions,
		TokensUsed:        tokens,
	}, nil
}

// extractInstructions parses the <instructions>...</instructions> JSON block
// that the system prompt instructs Claude to emit at the end of its response.
func extractInstructions(steps []models.TaskStep) []models.Instruction {
	// Find the last "thought" step — that's where Claude puts the final JSON
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].Type != "thought" {
			continue
		}

		text := steps[i].Description
		re := regexp.MustCompile(`(?s)<instructions>(.*?)</instructions>`)
		m := re.FindStringSubmatch(text)
		if m == nil {
			continue
		}

		var payload struct {
			Task  string               `json:"task"`
			Steps []models.Instruction `json:"steps"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(m[1])), &payload); err != nil {
			continue
		}
		return payload.Steps
	}
	return nil
}

func generateID() string {
	// Simple deterministic ID for a prototype — swap for uuid in production
	return fmt.Sprintf("task-%d", taskCounter())
}

var counter int

func taskCounter() int {
	counter++
	return counter
}
