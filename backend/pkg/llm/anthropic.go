package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"ui-agent/internal/models"
)

const anthropicAPIURL = "https://api.anthropic.com/v1/messages"
const model = "claude-sonnet-4-20250514"

// AnthropicClient wraps the Anthropic Messages API
type AnthropicClient struct {
	apiKey     string
	httpClient *http.Client
}

func NewAnthropicClient(apiKey string) *AnthropicClient {
	return &AnthropicClient{
		apiKey:     apiKey,
		httpClient: &http.Client{},
	}
}

// --- Request/Response shapes for the Anthropic API ---

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string OR []contentBlock
}

type contentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

type anthropicResponse struct {
	ID         string         `json:"id"`
	StopReason string         `json:"stop_reason"`
	Content    []contentBlock `json:"content"`
	Usage      usageBlock     `json:"usage"`
}

type usageBlock struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Complete performs an agentic loop:
//  1. Sends the user prompt + available tools to Claude
//  2. If Claude returns tool_use blocks, calls executeTool for each and continues
//  3. Accumulates TaskSteps for the frontend to render
//  4. Returns when stop_reason == "end_turn"
func (c *AnthropicClient) Complete(
	userPrompt string,
	tools []models.MCPTool,
	executeTool func(models.MCPToolCall) models.MCPToolResult,
) ([]models.TaskStep, int, error) {

	systemPrompt := buildSystemPrompt()

	// Convert MCP tools → Anthropic tool format
	var anthropicTools []anthropicTool
	for _, t := range tools {
		anthropicTools = append(anthropicTools, anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}

	messages := []anthropicMessage{
		{Role: "user", Content: userPrompt},
	}

	var steps []models.TaskStep
	totalTokens := 0
	stepNum := 0

	for {
		resp, err := c.callAPI(systemPrompt, messages, anthropicTools)
		if err != nil {
			return nil, 0, err
		}
		totalTokens += resp.Usage.InputTokens + resp.Usage.OutputTokens

		// Process each content block in the response
		var assistantContentBlocks []contentBlock
		var toolResults []contentBlock

		for _, block := range resp.Content {
			assistantContentBlocks = append(assistantContentBlocks, block)

			switch block.Type {
			case "text":
				stepNum++
				steps = append(steps, models.TaskStep{
					StepNumber:  stepNum,
					Type:        "thought",
					Title:       "Reasoning",
					Description: block.Text,
				})

			case "tool_use":
				stepNum++
				toolCall := models.MCPToolCall{
					ID:    block.ID,
					Name:  block.Name,
					Input: block.Input,
				}
				steps = append(steps, models.TaskStep{
					StepNumber: stepNum,
					Type:       "tool_call",
					Title:      fmt.Sprintf("Calling tool: %s", block.Name),
					ToolName:   block.Name,
					ToolInput:  block.Input,
				})

				// Execute the tool
				result := executeTool(toolCall)
				stepNum++
				steps = append(steps, models.TaskStep{
					StepNumber: stepNum,
					Type:       "tool_result",
					Title:      fmt.Sprintf("Result from: %s", block.Name),
					ToolName:   block.Name,
					ToolOutput: result.Content,
				})

				toolResults = append(toolResults, contentBlock{
					Type:      "tool_result",
					ToolUseID: result.ToolCallID,
					Content:   result.Content,
				})
			}
		}

		// Append assistant turn to conversation history
		messages = append(messages, anthropicMessage{
			Role:    "assistant",
			Content: assistantContentBlocks,
		})

		// If there were tool calls, feed results back and continue the loop
		if len(toolResults) > 0 {
			messages = append(messages, anthropicMessage{
				Role:    "user",
				Content: toolResults,
			})
		}

		// Stop when Claude is done
		if resp.StopReason == "end_turn" || len(toolResults) == 0 {
			break
		}
	}

	return steps, totalTokens, nil
}

func (c *AnthropicClient) callAPI(system string, messages []anthropicMessage, tools []anthropicTool) (*anthropicResponse, error) {
	reqBody := anthropicRequest{
		Model:     model,
		MaxTokens: 4096,
		System:    system,
		Messages:  messages,
		Tools:     tools,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", anthropicAPIURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, string(body))
	}

	var result anthropicResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// buildSystemPrompt returns the Chain-of-Thought + structured output prompt
func buildSystemPrompt() string {
	return `You are a UI Analysis Agent. Your job is to analyze user interface files and generate precise, structured, step-by-step task instructions.

BEHAVIOR:
1. When given a user task, first THINK through what UI elements are needed (write your reasoning as plain text).
2. Use the available tools to inspect the UI file(s) to locate relevant elements.
3. After gathering information, produce a final JSON block with structured instructions.

OUTPUT FORMAT:
Always end your response with a JSON block in this exact format:
<instructions>
{
  "task": "<user's original request>",
  "steps": [
    {
      "order": 1,
      "action": "NAVIGATE",
      "target": "Login Page",
      "description": "Open the application and navigate to the login screen",
      "selector": ""
    },
    {
      "order": 2,
      "action": "CLICK",
      "target": "Forgot Password link",
      "description": "Click the 'Forgot Password?' link below the login form",
      "selector": "a.forgot-password"
    }
  ]
}
</instructions>

Valid action types: NAVIGATE, CLICK, TYPE, SELECT, SCROLL, WAIT, VERIFY, SUBMIT

RULES:
- Always use tools before answering — do not guess at selectors or element names.
- Keep descriptions concise and written for a non-technical end user.
- Number steps sequentially starting at 1.
- If the UI file doesn't contain a needed element, note it in your reasoning and skip that selector.`
}
