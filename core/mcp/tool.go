package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/justin06lee/caveira/core/tools"
)

// ToolPrefix starts the name of every MCP tool the model sees.
const ToolPrefix = "mcp__"

var unsafeRE = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// toolName is mcp__<server>__<tool> in the characters function names may
// use, at most 64 long, and not one of taken.
func toolName(server, tool string, taken map[string]bool) string {
	name := ToolPrefix + unsafeRE.ReplaceAllString(server, "_") + "__" + unsafeRE.ReplaceAllString(tool, "_")
	if len(name) > 64 || taken[name] {
		h := sha256.Sum256([]byte(server + "\x00" + tool))
		suffix := "_" + hex.EncodeToString(h[:])[:6]
		name = name[:min(len(name), 64-len(suffix))] + suffix
	}
	return name
}

// SplitName is the server and the tool in an MCP tool's name as the
// model sees it, or ok false for any other tool.
func SplitName(name string) (server, tool string, ok bool) {
	rest, ok := strings.CutPrefix(name, ToolPrefix)
	if !ok {
		return "", "", false
	}
	server, tool, ok = strings.Cut(rest, "__")
	return server, tool, ok
}

// mcpTool is one server tool, offered to the model like a built-in one.
// Calls go through approval like shell commands, and "always allow"
// covers the server in this project.
type mcpTool struct {
	name    string
	server  string
	tool    *sdk.Tool
	session *sdk.ClientSession
	workDir string
}

func (t *mcpTool) Name() string     { return t.name }
func (t *mcpTool) Group() string    { return t.server }
func (t *mcpTool) Kind() tools.Kind { return tools.KindExecute }

func (t *mcpTool) Description() string {
	d := strings.TrimSpace(t.tool.Description)
	if len(d) > 2000 {
		d = d[:2000] + "…"
	}
	return fmt.Sprintf("[MCP server %s] %s", t.server, d)
}

func (t *mcpTool) Schema() map[string]any {
	var s map[string]any
	switch v := t.tool.InputSchema.(type) {
	case map[string]any:
		s = v
	default:
		if b, err := json.Marshal(v); err == nil {
			_ = json.Unmarshal(b, &s)
		}
	}
	if s == nil {
		s = map[string]any{}
	}
	// Some models reject an object schema without properties.
	if _, ok := s["type"]; !ok {
		s["type"] = "object"
	}
	if _, ok := s["properties"]; !ok {
		s["properties"] = map[string]any{}
	}
	return s
}

func (t *mcpTool) Preview(args json.RawMessage) string {
	a := strings.TrimSpace(string(args))
	if a == "" || a == "{}" || a == "null" {
		return t.server + " · " + t.tool.Name
	}
	if len(a) > 160 {
		a = a[:160] + "…"
	}
	return t.server + " · " + t.tool.Name + " " + a
}

func (t *mcpTool) AlwaysAllowed() bool { return Allowed(t.workDir, t.server) }
func (t *mcpTool) AllowAlways() error  { return Allow(t.workDir, t.server) }
func (t *mcpTool) AlwaysScope() string { return "the " + t.server + " server in this project" }

func (t *mcpTool) Run(ctx context.Context, args json.RawMessage) tools.Result {
	var in map[string]any
	if err := json.Unmarshal(args, &in); err != nil {
		return tools.Result{Output: "Error: the arguments must be a JSON object: " + err.Error(), Summary: "bad arguments", IsError: true}
	}
	res, err := t.session.CallTool(ctx, &sdk.CallToolParams{Name: t.tool.Name, Arguments: in})
	if err != nil {
		return tools.Result{Output: "Error: " + err.Error(), Summary: firstLine(err.Error()), IsError: true}
	}
	out := ResultText(res)
	bounded := tools.Bound(out, tools.OutputLimit)
	summary := fmt.Sprintf("%d lines", strings.Count(strings.TrimRight(out, "\n"), "\n")+1)
	if strings.TrimSpace(out) == "" {
		summary = "no output"
	}
	if res.IsError {
		summary = "error · " + firstLine(out)
		bounded = "Error: " + bounded
	}
	return tools.Result{Output: bounded, Summary: summary, IsError: res.IsError}
}

// ResultText is a tool result as text for the model: text as is, and a
// line standing in for anything it cannot read.
func ResultText(res *sdk.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		switch c := c.(type) {
		case *sdk.TextContent:
			parts = append(parts, c.Text)
		case *sdk.ImageContent:
			parts = append(parts, fmt.Sprintf("[image, %s, %d bytes]", c.MIMEType, len(c.Data)))
		case *sdk.AudioContent:
			parts = append(parts, fmt.Sprintf("[audio, %s, %d bytes]", c.MIMEType, len(c.Data)))
		case *sdk.ResourceLink:
			parts = append(parts, fmt.Sprintf("[resource %s %s]", c.URI, c.Name))
		case *sdk.EmbeddedResource:
			if c.Resource != nil && c.Resource.Text != "" {
				parts = append(parts, c.Resource.Text)
			} else if c.Resource != nil {
				parts = append(parts, fmt.Sprintf("[resource %s, %s]", c.Resource.URI, c.Resource.MIMEType))
			}
		}
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		if b, err := json.MarshalIndent(res.StructuredContent, "", "  "); err == nil {
			parts = append(parts, string(b))
		}
	}
	return strings.Join(parts, "\n")
}
