package computer

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// agentDesktopCatalogJSON maps each command of the agent-desktop command table to the xmesh tool that
// uses it. The catalog is the only source of exposure: a command without a tool is unmapped, the adapter
// refuses to run it, and it is listed in Info.unmapped_commands. A command that a new upstream release
// adds therefore never reaches MCP or cloud clients until someone maps it here and implements it.
//
//go:embed agentdesktop_catalog.json
var agentDesktopCatalogJSON []byte

// Risk classes of an upstream command. A mapped command's class is recorded so its scope can be
// chosen when it is mapped; clipboard, app_control and system commands are expected to default off.
const (
	riskObserve    = "observe"
	riskPointer    = "pointer"
	riskKeyboard   = "keyboard"
	riskText       = "text"
	riskClipboard  = "clipboard"
	riskAppControl = "app_control"
	riskSystem     = "system"
)

var riskClasses = []string{riskObserve, riskPointer, riskKeyboard, riskText, riskClipboard, riskAppControl, riskSystem}

// errUnmappedCommand refuses a command the catalog does not map. It is returned before the CLI runs.
// Callers that issue actions treat it as unknown, the conservative outcome, though nothing was sent.
var errUnmappedCommand = errors.New("agent-desktop command is not mapped")

type adCommand struct {
	Name string `json:"name"`
	Risk string `json:"risk"`
	Tool string `json:"tool,omitempty"`
	Op   string `json:"op,omitempty"`
}

type adCatalog struct {
	Schema       int                 `json:"schema"`
	TableVersion string              `json:"table_version"`
	Source       string              `json:"source"`
	ToolRequires map[string][]string `json:"tool_requires"`
	Commands     []adCommand         `json:"commands"`

	byName map[string]adCommand
}

var (
	adCommandName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	adToolName    = regexp.MustCompile(`^computer\.[a-z_]+$`)
)

// parseADCatalog decodes and checks the catalog. Unknown keys are rejected so a misspelt field
// (for example "tols") cannot silently leave a command unmapped or unrestricted.
func parseADCatalog(raw []byte) (*adCatalog, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var c adCatalog
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("agent-desktop catalog: %w", err)
	}
	if c.Schema != 1 {
		return nil, fmt.Errorf("agent-desktop catalog: schema %d is not supported", c.Schema)
	}
	if c.TableVersion == "" {
		return nil, errors.New("agent-desktop catalog: table_version is empty")
	}
	for tool, caps := range c.ToolRequires {
		if !adToolName.MatchString(tool) || len(caps) == 0 {
			return nil, fmt.Errorf("agent-desktop catalog: tool_requires %q is malformed", tool)
		}
	}
	c.byName = make(map[string]adCommand, len(c.Commands))
	for _, cmd := range c.Commands {
		if !adCommandName.MatchString(cmd.Name) {
			return nil, fmt.Errorf("agent-desktop catalog: command name %q is malformed", cmd.Name)
		}
		if _, dup := c.byName[cmd.Name]; dup {
			return nil, fmt.Errorf("agent-desktop catalog: command %q appears twice", cmd.Name)
		}
		if !slices.Contains(riskClasses, cmd.Risk) {
			return nil, fmt.Errorf("agent-desktop catalog: command %q has unknown risk %q", cmd.Name, cmd.Risk)
		}
		if cmd.Tool != "" && !adToolName.MatchString(cmd.Tool) {
			return nil, fmt.Errorf("agent-desktop catalog: command %q maps to malformed tool %q", cmd.Name, cmd.Tool)
		}
		if (cmd.Op != "") != (cmd.Tool == "computer.ref_act") {
			return nil, fmt.Errorf("agent-desktop catalog: command %q must carry op exactly when it maps to computer.ref_act", cmd.Name)
		}
		c.byName[cmd.Name] = cmd
	}
	return &c, nil
}

// mustLoadADCatalog panics on a malformed embedded catalog. The catalog is compiled into the binary,
// so a malformed one is a build defect that the package tests catch before release.
func mustLoadADCatalog() *adCatalog {
	c, err := parseADCatalog(agentDesktopCatalogJSON)
	if err != nil {
		panic(err)
	}
	return c
}

var adCatalogTable = mustLoadADCatalog()

// mapped returns the catalog entry for the command that args invoke. The global --headed flag may
// precede the command. Any other leading flag is refused: the command is never guessed from a later token.
func (c *adCatalog) mapped(args []string) (adCommand, bool) {
	for _, arg := range args {
		if arg == "--headed" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return adCommand{}, false
		}
		cmd, ok := c.byName[arg]
		return cmd, ok && cmd.Tool != ""
	}
	return adCommand{}, false
}

// unmapped lists the upstream commands with no xmesh tool, sorted. It is the value of Info.unmapped_commands.
func (c *adCatalog) unmapped() []string {
	var out []string
	for _, cmd := range c.Commands {
		if cmd.Tool == "" {
			out = append(out, cmd.Name)
		}
	}
	slices.Sort(out)
	return out
}

// commandForOp returns the upstream command that implements one op of computer.ref_act.
func (c *adCatalog) commandForOp(op string) (string, bool) {
	for _, cmd := range c.Commands {
		if cmd.Tool == "computer.ref_act" && cmd.Op == op {
			return cmd.Name, true
		}
	}
	return "", false
}

// requires returns the engine capabilities a tool needs before it is listed.
func (c *adCatalog) requires(tool string) []string {
	return c.ToolRequires[tool]
}

// MappedOps returns the ops that tool maps to, in catalog order. Only computer.ref_act carries ops. The agent
// package checks its schema enum against this list, so an op cannot be advertised without a command behind it.
func MappedOps(tool string) []string {
	var ops []string
	for _, cmd := range adCatalogTable.Commands {
		if cmd.Tool == tool && cmd.Op != "" {
			ops = append(ops, cmd.Op)
		}
	}
	return ops
}

// MappedTools returns the xmesh tools that at least one upstream command maps to, sorted and unique.
func MappedTools() []string {
	var tools []string
	for _, cmd := range adCatalogTable.Commands {
		if cmd.Tool != "" && !slices.Contains(tools, cmd.Tool) {
			tools = append(tools, cmd.Tool)
		}
	}
	slices.Sort(tools)
	return tools
}
