# UI Analysis Agent — Technical Design & Prototype

An AI agent that reads local mock UI files and generates structured, step-by-step task instructions from natural language prompts.

**Stack:** Go (Gin) · Next.js (React) · Claude Sonnet via Anthropic API · MCP (Model Context Protocol)

---

## Table of Contents

1. [Project Structure](#project-structure)
2. [LLM Selection & Prompt Engineering](#2-llm-selection--prompt-engineering)
3. [Agentic Tooling & MCP Server Design](#3-agentic-tooling--mcp-server-design)
4. [Fullstack Implementation](#4-fullstack-implementation)
5. [Evaluation & Performance Tuning](#5-evaluation--performance-tuning)
6. [Running the Project](#running-the-project)

---

## Project Structure

```
ui-agent-project/
├── backend/
│   ├── cmd/server/
│   │   └── main.go               # Entry point: wires LLM, MCP, Gin
│   ├── internal/
│   │   ├── agent/
│   │   │   └── agent.go          # Agentic loop orchestrator
│   │   ├── api/
│   │   │   └── routes.go         # Gin HTTP handlers
│   │   ├── mcp/
│   │   │   └── server.go         # MCP tool executor (file sandbox)
│   │   └── models/
│   │       └── models.go         # Shared data types
│   └── pkg/llm/
│       └── anthropic.go          # Anthropic API client + tool-use loop
│
├── frontend/
│   └── src/
│       ├── app/
│       │   └── page.jsx          # Next.js root page
│       ├── components/agent/
│       │   ├── AgentPanel.jsx    # Main UI shell
│       │   ├── StepFeed.jsx      # Reasoning trace renderer
│       │   └── InstructionList.jsx # Final instructions renderer
│       └── lib/
│           └── api.js            # Typed fetch helpers
│
├── mock-ui-files/
│   ├── login.html                # Sample login UI
│   └── reset-password.html      # Sample password reset flow
│
├── .env.example
└── README.md
```

---

## 2. LLM Selection & Prompt Engineering

### Model Choice: Claude Sonnet (claude-sonnet-4-20250514)

**Justification:**

| Criterion | Why Claude Sonnet wins here |
|---|---|
| **Tool use / agentic behavior** | Native tool-use support with a well-defined `tool_use` / `tool_result` content block format. No prompt-hacking required. |
| **Structured output adherence** | Reliably follows formatting constraints (e.g., emit a JSON block inside `<instructions>` tags) with minimal jailbreak risk. |
| **Reasoning depth** | Sonnet sits between Haiku (fast/cheap) and Opus (slow/expensive) — the sweet spot for multi-step UI analysis that needs real chain-of-thought but not research-level reasoning. |
| **Latency** | ~1–3 s TTFT for typical prompts; acceptable for a synchronous task endpoint. |
| **Cost** | ~$3 / 1M input tokens, ~$15 / 1M output tokens — significantly cheaper than GPT-4o at equivalent capability for tool-use tasks. |

**Why not GPT-4o?**
GPT-4o has comparable tool-use quality but higher per-token cost and slightly less reliable structured-output compliance on nested JSON. Gemini 1.5 Pro has an excellent context window but weaker tool-use consistency at the time of writing.

---

### Prompting Strategy

**Two-layer approach: System Prompt (Chain-of-Thought) + Output Schema Enforcement**

#### Layer 1 — Chain-of-Thought system prompt

The system prompt instructs Claude to reason *before* acting:

```
1. THINK: Write out which UI elements are likely needed (free text).
2. ACT:   Call tools to verify those elements exist and get their selectors.
3. OUTPUT: Emit a strict JSON block inside <instructions> tags.
```

This mirrors the ReAct (Reason + Act) pattern. By explicitly separating the reasoning phase from the tool-call phase from the output phase, we get:
- More accurate element identification (agent checks before asserting)
- A transparent audit trail rendered in the frontend StepFeed
- Predictable output structure for downstream parsing

#### Layer 2 — Output schema enforcement

The system prompt provides a verbatim JSON template the model must fill:

```json
{
  "task": "<original request>",
  "steps": [
    { "order": 1, "action": "NAVIGATE", "target": "...", "description": "...", "selector": "" }
  ]
}
```

Valid `action` values are enumerated (`NAVIGATE`, `CLICK`, `TYPE`, `SELECT`, `SCROLL`, `WAIT`, `VERIFY`, `SUBMIT`), reducing hallucinated action types. The output is wrapped in `<instructions>...</instructions>` delimiters so the backend can extract it with a simple regex even if Claude adds surrounding prose.

**Why not pure JSON mode / structured outputs?**
Tool-use calls and structured output mode conflict in the current Anthropic API — you can't use both simultaneously. The delimiter-based extraction is a reliable workaround and also gives Claude freedom to add natural reasoning before the final output.

---

## 3. Agentic Tooling & MCP Server Design

### MCP Server Architecture

The MCP server (`backend/internal/mcp/server.go`) acts as a **sandboxed file-system gateway** between the LLM and the local UI files. The LLM has zero direct access to the filesystem — every read goes through `Server.Execute()`.

```
LLM (Claude)
    │  tool_use: { name: "find_elements", input: { filename: "login.html", query: "submit button" } }
    ▼
Agent.Run() → mcp.Server.Execute(toolCall)
    │  bounds-checks filename (filepath.Base prevents path traversal)
    │  reads file from ./mock-ui-files/
    │  parses HTML, returns matching elements as JSON
    ▼
MCPToolResult { content: '{ "matches": [...] }' }
    │
    ▼
LLM receives tool_result, continues reasoning
```

### Exposed Tools

| Tool | Purpose | Key inputs |
|---|---|---|
| `list_ui_files` | Discover available files | — |
| `read_ui_file` | Read raw file content | `filename` |
| `find_elements` | Semantic element search | `filename`, `query` |
| `extract_all_elements` | Full element inventory | `filename` |

### Autonomous Decision Making

The agent loop in `pkg/llm/anthropic.go` implements the standard tool-use loop:

```
1. Send user prompt + tool manifest to Claude
2. Claude returns content blocks (text and/or tool_use)
3. For each tool_use block:
   a. Record a "tool_call" step for the frontend
   b. Execute via mcp.Server.Execute()
   c. Record a "tool_result" step
   d. Append tool_result to the conversation as a user turn
4. Send the updated conversation back to Claude
5. Repeat until stop_reason == "end_turn"
```

Claude autonomously decides:
- Whether to call `list_ui_files` first (discovery) or go directly to `find_elements` (if the filename is obvious from the prompt)
- How many tool calls are needed (one targeted `find_elements`, or a broad `extract_all_elements` followed by selective filtering)
- When it has enough information to produce the final `<instructions>` block

**Security considerations for production:**
- `filepath.Base()` prevents directory traversal on all filenames
- The sandbox root is an explicit constructor parameter — never derived from user input
- File size is capped at 8 000 characters before being sent to the LLM (prevents token overflow)
- In production: add an allowlist of file extensions (`.html`, `.json`) and a per-request rate limit

---

## 4. Fullstack Implementation

### Pipeline Overview

```
User types prompt
      │
      ▼
[Next.js Frontend]
 AgentPanel.jsx
 POST /api/v1/task  ──────────────────────────────────────────────────────┐
                                                                           │
                                                               [Go Backend / Gin]
                                                               api/routes.go
                                                                     │
                                                               agent/agent.go
                                                               (orchestrates loop)
                                                                     │
                                                          ┌──────────┴──────────┐
                                                          │                     │
                                                   pkg/llm/                internal/mcp/
                                                   anthropic.go             server.go
                                                   (Anthropic API)          (file tools)
                                                          │                     │
                                                          └──────────┬──────────┘
                                                                     │
                                                              TaskResponse
                                                              { steps[], final_instructions[] }
                                                                           │
      ┌────────────────────────────────────────────────────────────────────┘
      │
[Next.js Frontend]
 StepFeed.jsx       ← renders agent reasoning trace
 InstructionList.jsx ← renders structured instructions
```

### Backend (Go / Gin)

**`GET /api/v1/health`** — liveness probe
**`GET /api/v1/files`** — returns list of mock UI files (used by frontend file picker)
**`POST /api/v1/task`** — core endpoint

Request body:
```json
{ "prompt": "How do I reset my password?", "ui_file_id": "reset-password.html" }
```

Response:
```json
{
  "task_id": "task-1",
  "original_prompt": "How do I reset my password?",
  "steps": [
    { "step_number": 1, "type": "thought", "title": "Reasoning", "description": "I need to find the password reset flow..." },
    { "step_number": 2, "type": "tool_call", "title": "Calling tool: find_elements", "tool_name": "find_elements", "tool_input": { "filename": "reset-password.html", "query": "send reset link" } },
    { "step_number": 3, "type": "tool_result", "title": "Result from: find_elements", "tool_output": "{ \"matches\": [...] }" },
    ...
  ],
  "final_instructions": [
    { "order": 1, "action": "NAVIGATE", "target": "Login Page", "description": "Go to the login page", "selector": "" },
    { "order": 2, "action": "CLICK",    "target": "Forgot password? link", "description": "Click the 'Forgot password?' link below the sign-in form", "selector": "#forgot-password-link" },
    { "order": 3, "action": "TYPE",     "target": "Email Address field", "description": "Enter your registered email address", "selector": "#reset-email" },
    { "order": 4, "action": "CLICK",    "target": "Send Reset Link button", "description": "Click the button to receive the reset email", "selector": "#send-reset-btn" },
    { "order": 5, "action": "NAVIGATE", "target": "Email inbox", "description": "Open the reset email and click the link inside", "selector": "" },
    { "order": 6, "action": "TYPE",     "target": "New Password field", "description": "Enter your new password (minimum 8 characters)", "selector": "#new-password" },
    { "order": 7, "action": "CLICK",    "target": "Update Password button", "description": "Submit your new password", "selector": "#set-password-btn" }
  ],
  "tokens_used": 1842
}
```

### Frontend (Next.js)

**Component tree:**
```
page.jsx
  └── AgentPanel.jsx       (state owner: prompt, result, loading)
        ├── <select>       (file picker, populated from GET /files)
        ├── <textarea>     (prompt input)
        ├── StepFeed       (renders steps[])
        └── InstructionList (renders final_instructions[])
```

**StepFeed** color-codes each step type:
- `thought` → gray card (Claude's internal reasoning)
- `tool_call` → blue card (what tool Claude decided to invoke)
- `tool_result` → purple card (what the tool returned)
- `instruction` → green card (completion signal)

**InstructionList** renders the final procedural output as a numbered list with action-type badges color-coded by verb (CLICK = yellow, TYPE = green, NAVIGATE = sky, etc.).

### Task Flow Example: "How do I reset my password?"

```
1. User submits prompt
2. Go handler calls agent.Run({ prompt: "How do I reset my password?" })
3. Agent sends to Claude: prompt + 4 tool definitions
4. Claude (step 1 - thought): "I should look for a password reset page. Let me list available files first."
5. Claude (step 2 - tool_call): list_ui_files {}
6. MCP returns: { "files": ["login.html", "reset-password.html"] }
7. Claude (step 3 - thought): "reset-password.html is relevant. Let me extract its elements."
8. Claude (step 4 - tool_call): extract_all_elements { filename: "reset-password.html" }
9. MCP parses HTML, returns all buttons/inputs/links with selectors
10. Claude (step 5 - thought + instructions block):
    Reasons about the 3-step flow (request → email → new password)
    Emits <instructions>{ "steps": [ ...7 steps... ] }</instructions>
11. Agent parses <instructions> block → []Instruction
12. Response sent to frontend
13. Frontend renders 5 reasoning steps in StepFeed + 7 instructions in InstructionList
```

---

## 5. Evaluation & Performance Tuning

### Quantitative Metrics

| Metric | How to measure | Target |
|---|---|---|
| **Instruction accuracy** | Human-labeled golden set of (prompt, UI file) → expected steps. Compare generated steps vs expected using exact-match and ROUGE-L on descriptions. | ROUGE-L ≥ 0.75 |
| **Selector precision** | For each generated selector, run `document.querySelector(selector)` against the source HTML. Count hits/misses. | ≥ 90% valid selectors |
| **Step count delta** | `abs(generated_count - expected_count)` — penalizes both over- and under-generation | Avg delta ≤ 1.5 steps |
| **Tool call efficiency** | Number of tool calls per task. Fewer is better if accuracy is maintained. | ≤ 3 calls / task |
| **Latency (P50/P95)** | End-to-end wall time from POST to response | P50 ≤ 4 s, P95 ≤ 10 s |
| **Token usage** | Input + output tokens per task | Track for cost budgeting |

### Qualitative Metrics

- **Readability review:** Are descriptions clear to a non-technical user? Scored 1–5 by human reviewers on a weekly sample.
- **Task coverage:** Does the agent handle edge cases (element not found, multi-page flows, ambiguous prompts)? Reviewed via error log sampling.
- **Hallucination rate:** Count of steps referencing elements that don't exist in the UI file. Caught by selector validation above.

### Regression Testing

Maintain a `/tests/fixtures/` directory of golden (prompt, ui_file, expected_instructions) triplets. Run on every CI push:

```
go test ./internal/agent/... -run TestGoldenSet
```

### Resource Monitoring

**Application-level:**
- Wrap each `callAPI()` invocation with a timer. Export as a Prometheus histogram: `llm_request_duration_seconds`.
- Track token usage per request: `llm_tokens_total{type="input|output"}`.
- Track tool call counts: `mcp_tool_calls_total{tool="find_elements|..."}`.
- Expose a `/metrics` endpoint from the Go server for Prometheus scraping.

**Infrastructure-level (production):**
- Deploy behind a load balancer; monitor CPU and memory with standard cloud metrics (Cloud Watch / GCP Monitoring).
- The Go backend itself is CPU-light (all heavy compute is offloaded to Anthropic's API). The primary resource concern is **goroutine count** (one per in-flight request) and **HTTP connection pool** size to the Anthropic API.
- Set `GOMAXPROCS` to match vCPU count. Keep `MaxIdleConnsPerHost` tuned for Anthropic API concurrency.
- For GPU/local model variants: monitor VRAM utilization with `nvidia-smi dmon` and expose via `dcgm-exporter`.

**Optimization levers:**
- Cache `list_ui_files` results (files don't change at runtime) to eliminate one tool call per cold session.
- Pre-parse all UI files into `[]UIElement` on server startup; `find_elements` then runs in memory instead of re-reading disk.
- Add a response cache keyed on `sha256(prompt + filename)` for identical repeated queries.
- Reduce max_tokens from 4096 to ~2048 for simple one-file tasks to cut output token cost by ~30%.

---

## Running the Project

### Prerequisites

- Go 1.22+
- Node.js 20+
- An Anthropic API key

However due to the nature of this prototype being a pipeline example rather than a functioning product this is not possible without investing some more time in its base functions.