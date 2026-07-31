package seat

import (
	"fmt"
	"strings"
)

// Steam records which compatibility tool to run everything else with in its own
// configuration file, under a path four blocks deep, and there is no command
// that sets it. So the file is edited.
//
// Edited rather than rewritten, and that is the whole design here. config.vdf
// holds the account this seat is signed in as, the shader cache state and a
// list of content servers, and a seat whose Steam configuration was replaced by
// a generated one would be a seat somebody has to sign in to again. Everything
// below returns the input bytes with one span inserted or one value replaced,
// so anything this code does not understand comes out exactly as it went in.
const (
	// compatMappingKey is where the mapping lives inside the Steam block.
	compatMappingKey = "CompatToolMapping"

	// compatGlobalKey is the application id that means "everything else". Steam
	// writes a real id here for a per game override; zero is the global one,
	// which is what the Steam Play setting in the interface writes.
	compatGlobalKey = "0"
)

// vdfNode is one block or one pair out of a Steam configuration file.
type vdfNode struct {
	key string

	// afterBrace is the offset just past this block's opening brace, which is
	// where a new child is inserted. Zero for a pair.
	afterBrace int

	// indent is the indentation of this block's own key line, so that anything
	// inserted into it lines up with what is already there. Steam rewrites the
	// file itself soon enough, but a file that reads as though a program had
	// been at it invites somebody to undo it by hand.
	indent string

	// valueStart and valueEnd bracket the value of a pair, quotes included.
	valueStart, valueEnd int

	block    bool
	children []*vdfNode
}

// child finds a direct child by key, case insensitively, because Steam is not
// consistent about it and a miss here would silently add a second block beside
// the one that already exists.
func (n *vdfNode) child(key string) *vdfNode {
	for _, c := range n.children {
		if strings.EqualFold(c.key, key) {
			return c
		}
	}

	return nil
}

// parseVDF reads the structure and remembers where everything sits.
//
// A deliberately small reader rather than a general implementation of the
// format, in the same spirit as the manifest reader in internal/library: it
// only has to be right about quoted strings, braces and comments, because that
// is all Steam writes here.
func parseVDF(data string) (*vdfNode, error) {
	root := &vdfNode{block: true}
	stack := []*vdfNode{root}

	i, line := 0, 0

	// pending is a key that has been read and is waiting to find out whether it
	// is a block or the left half of a pair.
	var pending string

	var pendingIndent string

	for i < len(data) {
		switch {
		case data[i] == '\n':
			line++
			i++
		case data[i] == ' ' || data[i] == '\t' || data[i] == '\r':
			i++
		case strings.HasPrefix(data[i:], "//"):
			for i < len(data) && data[i] != '\n' {
				i++
			}
		case data[i] == '{':
			if pending == "" {
				return nil, fmt.Errorf("a block with no name at line %d", line+1)
			}

			node := &vdfNode{key: pending, block: true, afterBrace: i + 1, indent: pendingIndent}
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, node)
			stack = append(stack, node)
			pending = ""
			i++
		case data[i] == '}':
			if len(stack) == 1 {
				return nil, fmt.Errorf("a closing brace with nothing open at line %d", line+1)
			}

			stack = stack[:len(stack)-1]
			i++
		case data[i] == '"':
			start := i
			i++

			for i < len(data) && data[i] != '"' {
				if data[i] == '\\' && i+1 < len(data) {
					i++
				}

				i++
			}

			if i >= len(data) {
				return nil, fmt.Errorf("a string that never ends at line %d", line+1)
			}

			i++
			text := data[start+1 : i-1]

			if pending == "" {
				pending = text
				pendingIndent = indentBefore(data, start)

				continue
			}

			// The second string on a line is a value, so what came before it
			// was a pair rather than a block.
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, &vdfNode{
				key: pending, valueStart: start, valueEnd: i, indent: pendingIndent,
			})
			pending = ""
		default:
			return nil, fmt.Errorf("unexpected %q at line %d", data[i], line+1)
		}
	}

	if len(stack) != 1 {
		return nil, fmt.Errorf("%d blocks were never closed", len(stack)-1)
	}

	return root, nil
}

// indentBefore reports the whitespace at the start of the line offset sits on.
func indentBefore(data string, offset int) string {
	start := strings.LastIndexByte(data[:offset], '\n') + 1

	return data[start:offset]
}

// steamBlock finds the block Steam keeps its own settings in.
func steamBlock(root *vdfNode) *vdfNode {
	node := root

	for _, key := range []string{"InstallConfigStore", "Software", "Valve", "Steam"} {
		if node = node.child(key); node == nil {
			return nil
		}
	}

	return node
}

// SetCompatTool points Steam's "run everything else with" setting at a tool.
//
// It reports whether it changed anything, so that a caller can leave a file it
// did not have to touch alone rather than write it back identical.
//
// Somebody else's choice wins. If the setting names a tool that is not one of
// ours, it stays: a seat where the player picked Proton Experimental on purpose
// is not a seat with a broken setting, and provisioning that seat again should
// not quietly take the decision back. What does get rewritten is a setting that
// names one of our own builds under its old versioned name, because that is the
// same decision expressed in a way that stops working at the next update.
func SetCompatTool(data []byte, tool string) ([]byte, bool, error) {
	text := string(data)

	if strings.TrimSpace(text) == "" {
		return []byte(freshConfig(tool)), true, nil
	}

	root, err := parseVDF(text)
	if err != nil {
		return data, false, err
	}

	steam := steamBlock(root)
	if steam == nil {
		return data, false, fmt.Errorf("this is not a Steam configuration: it has no InstallConfigStore/Software/Valve/Steam")
	}

	mapping := steam.child(compatMappingKey)
	if mapping == nil {
		return []byte(insert(text, steam.afterBrace, mappingBlock(steam.indent+"\t", tool))), true, nil
	}

	global := mapping.child(compatGlobalKey)
	if global == nil {
		return []byte(insert(text, mapping.afterBrace, globalBlock(mapping.indent+"\t", tool))), true, nil
	}

	name := global.child("name")
	if name == nil {
		return []byte(insert(text, global.afterBrace,
			"\n"+global.indent+"\t"+quote("name")+"\t\t"+quote(tool))), true, nil
	}

	current := strings.Trim(text[name.valueStart:name.valueEnd], `"`)

	if current == tool {
		return data, false, nil
	}

	// Ours under a name that carries a version, which is the shape this used to
	// have and the reason the name is fixed now.
	if current != "" && !strings.HasPrefix(current, protonName) {
		return data, false, nil
	}

	return []byte(text[:name.valueStart] + quote(tool) + text[name.valueEnd:]), true, nil
}

// insert puts text at an offset, which is the only edit this file makes.
func insert(text string, at int, what string) string {
	return text[:at] + what + text[at:]
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// mappingBlock is the whole mapping, for a configuration that has none.
func mappingBlock(indent, tool string) string {
	return "\n" + indent + quote(compatMappingKey) +
		"\n" + indent + "{" +
		globalBlock(indent+"\t", tool) +
		"\n" + indent + "}"
}

// globalBlock is the entry for "everything else".
//
// priority is what Steam itself writes for a choice made in its interface. It
// decides which of several mappings wins, and a mapping written without one
// loses to every per game override, which is what it should do.
func globalBlock(indent, tool string) string {
	return "\n" + indent + quote(compatGlobalKey) +
		"\n" + indent + "{" +
		"\n" + indent + "\t" + quote("name") + "\t\t" + quote(tool) +
		"\n" + indent + "\t" + quote("config") + "\t\t" + quote("") +
		"\n" + indent + "\t" + quote("priority") + "\t\t" + quote("75") +
		"\n" + indent + "}"
}

// freshConfig is for a seat whose Steam has never run.
//
// Steam fills the rest in when it starts and keeps what it finds, so the file
// only has to carry the one setting and the four blocks it lives under.
func freshConfig(tool string) string {
	return quote("InstallConfigStore") + "\n{\n" +
		"\t" + quote("Software") + "\n\t{\n" +
		"\t\t" + quote("Valve") + "\n\t\t{\n" +
		"\t\t\t" + quote("Steam") + "\n\t\t\t{" +
		mappingBlock("\t\t\t\t", tool) +
		"\n\t\t\t}\n\t\t}\n\t}\n}\n"
}
