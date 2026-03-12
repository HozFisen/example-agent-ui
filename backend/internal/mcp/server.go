package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"ui-agent/internal/models"
)

// Server is the MCP tool executor. It owns the local file sandbox
// and exposes a fixed set of tools the LLM agent can call.
type Server struct {
	rootDir string
}

func NewServer(rootDir string) *Server {
	return &Server{rootDir: rootDir}
}

// Tools returns the tool manifest sent to the LLM on every request.
// The LLM reads these descriptions to decide which tool to call.
func (s *Server) Tools() []models.MCPTool {
	return []models.MCPTool{
		{
			Name:        "list_ui_files",
			Description: "Lists all available mock UI files in the local sandbox. Call this first to discover which files exist.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
				"required":   []string{},
			},
		},
		{
			Name:        "read_ui_file",
			Description: "Reads and returns the raw content of a specific UI file by filename. Use this to inspect the HTML/JSON structure.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filename": map[string]any{
						"type":        "string",
						"description": "The filename of the UI file to read (e.g. 'login.html')",
					},
				},
				"required": []string{"filename"},
			},
		},
		{
			Name:        "find_elements",
			Description: "Searches a UI file for interactive elements matching a query (e.g. 'login button', 'password field'). Returns matching elements with their selectors.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filename": map[string]any{
						"type":        "string",
						"description": "The UI file to search",
					},
					"query": map[string]any{
						"type":        "string",
						"description": "Natural language description of the element to find (e.g. 'submit button', 'email input')",
					},
				},
				"required": []string{"filename", "query"},
			},
		},
		{
			Name:        "extract_all_elements",
			Description: "Extracts and returns ALL interactive elements from a UI file as a structured list. Useful for a complete overview.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filename": map[string]any{
						"type":        "string",
						"description": "The UI file to extract elements from",
					},
				},
				"required": []string{"filename"},
			},
		},
	}
}

// Execute routes a tool call from the LLM to the correct handler.
// This is the ONLY entry point — the LLM cannot access the filesystem directly.
func (s *Server) Execute(call models.MCPToolCall) models.MCPToolResult {
	var content string

	switch call.Name {
	case "list_ui_files":
		content = s.listUIFiles()
	case "read_ui_file":
		filename, _ := call.Input["filename"].(string)
		content = s.readUIFile(filename)
	case "find_elements":
		filename, _ := call.Input["filename"].(string)
		query, _ := call.Input["query"].(string)
		content = s.findElements(filename, query)
	case "extract_all_elements":
		filename, _ := call.Input["filename"].(string)
		content = s.extractAllElements(filename)
	default:
		content = fmt.Sprintf(`{"error": "Unknown tool: %s"}`, call.Name)
	}

	return models.MCPToolResult{
		ToolCallID: call.ID,
		Content:    content,
	}
}

// --- Tool implementations ---

func (s *Server) listUIFiles() string {
	entries, err := os.ReadDir(s.rootDir)
	if err != nil {
		return `{"error": "Could not read UI files directory"}`
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() {
			files = append(files, e.Name())
		}
	}

	result, _ := json.Marshal(map[string]any{"files": files})
	return string(result)
}

func (s *Server) readUIFile(filename string) string {
	// Security: prevent path traversal
	clean := filepath.Base(filename)
	path := filepath.Join(s.rootDir, clean)

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf(`{"error": "File not found: %s"}`, clean)
	}

	// Truncate very large files to avoid token overflow
	content := string(data)
	if len(content) > 8000 {
		content = content[:8000] + "\n... [truncated]"
	}

	result, _ := json.Marshal(map[string]any{
		"filename": clean,
		"content":  content,
	})
	return string(result)
}

func (s *Server) findElements(filename, query string) string {
	clean := filepath.Base(filename)
	path := filepath.Join(s.rootDir, clean)

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf(`{"error": "File not found: %s"}`, clean)
	}

	elements := parseElements(string(data))
	queryLower := strings.ToLower(query)

	var matches []models.UIElement
	for _, el := range elements {
		labelLower := strings.ToLower(el.Label)
		typeLower := strings.ToLower(el.Type)

		if strings.Contains(labelLower, queryLower) ||
			strings.Contains(queryLower, typeLower) ||
			strings.Contains(queryLower, labelLower) {
			matches = append(matches, el)
		}
	}

	result, _ := json.Marshal(map[string]any{
		"query":   query,
		"matches": matches,
	})
	return string(result)
}

func (s *Server) extractAllElements(filename string) string {
	clean := filepath.Base(filename)
	path := filepath.Join(s.rootDir, clean)

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf(`{"error": "File not found: %s"}`, clean)
	}

	elements := parseElements(string(data))
	result, _ := json.Marshal(map[string]any{
		"filename": clean,
		"elements": elements,
	})
	return string(result)
}

// parseElements is a lightweight regex-based HTML element extractor.
// In production this would use a proper HTML parser (golang.org/x/net/html).
func parseElements(html string) []models.UIElement {
	var elements []models.UIElement

	// Match <button>, <input>, <a>, <select>, <textarea>
	patterns := []struct {
		tag     string
		elType  string
		pattern string
	}{
		{"button", "button", `<button([^>]*)>([^<]*)</button>`},
		{"input", "input", `<input([^>]*)/?>`},
		{"a", "link", `<a([^>]*)>([^<]*)</a>`},
		{"select", "select", `<select([^>]*)>`},
		{"textarea", "textarea", `<textarea([^>]*)>`},
	}

	for _, p := range patterns {
		re := regexp.MustCompile("(?i)" + p.pattern)
		matches := re.FindAllStringSubmatch(html, -1)

		for _, m := range matches {
			attrs := ""
			label := ""
			if len(m) > 1 {
				attrs = m[1]
			}
			if len(m) > 2 {
				label = strings.TrimSpace(m[2])
			}

			el := models.UIElement{
				Type:     p.elType,
				Label:    label,
				ID:       extractAttr(attrs, "id"),
				Class:    extractAttr(attrs, "class"),
				Selector: buildSelector(p.tag, attrs),
			}

			// For inputs, use placeholder or name as label
			if el.Label == "" {
				el.Label = extractAttr(attrs, "placeholder")
			}
			if el.Label == "" {
				el.Label = extractAttr(attrs, "name")
			}

			elements = append(elements, el)
		}
	}

	return elements
}

func extractAttr(attrs, name string) string {
	re := regexp.MustCompile(fmt.Sprintf(`%s=["']([^"']*)["']`, name))
	m := re.FindStringSubmatch(attrs)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

func buildSelector(tag, attrs string) string {
	id := extractAttr(attrs, "id")
	if id != "" {
		return "#" + id
	}
	class := extractAttr(attrs, "class")
	if class != "" {
		// Use first class only
		parts := strings.Fields(class)
		return tag + "." + parts[0]
	}
	return tag
}
