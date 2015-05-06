package kingpin

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

type TokenType int

// Token types.
const (
	TokenShort TokenType = iota
	TokenLong
	TokenArg
	TokenEOL
)

func (t TokenType) String() string {
	switch t {
	case TokenShort:
		return "short flag"
	case TokenLong:
		return "long flag"
	case TokenArg:
		return "argument"
	case TokenEOL:
		return "<EOL>"
	}
	return "?"
}

var (
	TokenEOLMarker = Token{-1, TokenEOL, ""}
)

type Token struct {
	Index int
	Type  TokenType
	Value string
}

func (t *Token) Equal(o *Token) bool {
	return t.Index == o.Index
}

func (t *Token) IsFlag() bool {
	return t.Type == TokenShort || t.Type == TokenLong
}

func (t *Token) IsEOF() bool {
	return t.Type == TokenEOL
}

func (t *Token) String() string {
	switch t.Type {
	case TokenShort:
		return "-" + t.Value
	case TokenLong:
		return "--" + t.Value
	case TokenArg:
		return t.Value
	case TokenEOL:
		return "<EOL>"
	default:
		panic("unhandled type")
	}
}

// A union of possible elements in a parse stack.
type ParseElement struct {
	// Clause is either *CmdClause, *ArgClause or *FlagClause.
	Clause interface{}
	// Value is corresponding value for an ArgClause or FlagClause (if any).
	Value *string
}

// ParseContext holds the current context of the parser. When passed to
// Action() callbacks Elements will be fully populated with *FlagClause,
// *ArgClause and *CmdClause values and their corresponding arguments (if
// any).
type ParseContext struct {
	SelectedCommand string
	argsOnly        bool
	peek            []*Token
	argi            int // Index of current command-line arg we're processing.
	args            []string
	flags           *flagGroup
	arguments       *argGroup
	argumenti       int // Cursor into arguments
	// Flags, arguments and commands encountered and collected during parse.
	Elements []*ParseElement
}

func (p *ParseContext) nextArg() *ArgClause {
	if p.argumenti >= len(p.arguments.args) {
		return nil
	}
	arg := p.arguments.args[p.argumenti]
	if !arg.consumesRemainder() {
		p.argumenti++
	}
	return arg
}

func (p *ParseContext) next() {
	p.argi++
	p.args = p.args[1:]
}

// HasTrailingArgs returns true if there are unparsed command-line arguments.
// This can occur if the parser can not match remaining arguments.
func (p *ParseContext) HasTrailingArgs() bool {
	return len(p.args) > 0
}

func tokenize(args []string) *ParseContext {
	return &ParseContext{
		args:      args,
		flags:     newFlagGroup(),
		arguments: newArgGroup(),
	}
}

func (p *ParseContext) mergeFlags(flags *flagGroup) {
	for _, flag := range flags.flagOrder {
		if flag.shorthand != 0 {
			p.flags.short[string(flag.shorthand)] = flag
		}
		p.flags.long[flag.name] = flag
		p.flags.flagOrder = append(p.flags.flagOrder, flag)
	}
}

func (p *ParseContext) mergeArgs(args *argGroup) {
	for _, arg := range args.args {
		p.arguments.args = append(p.arguments.args, arg)
	}
}

func (p *ParseContext) EOL() bool {
	return p.Peek().Type == TokenEOL
}

// Next token in the parse context.
func (p *ParseContext) Next() *Token {
	if len(p.peek) > 0 {
		return p.pop()
	}

	// End of tokens.
	if len(p.args) == 0 {
		return &Token{Index: p.argi, Type: TokenEOL}
	}

	arg := p.args[0]
	p.next()

	if p.argsOnly {
		return &Token{p.argi, TokenArg, arg}
	}

	// All remaining args are passed directly.
	if arg == "--" {
		p.argsOnly = true
		return p.Next()
	}

	if strings.HasPrefix(arg, "--") {
		parts := strings.SplitN(arg[2:], "=", 2)
		token := &Token{p.argi, TokenLong, parts[0]}
		if len(parts) == 2 {
			p.push(&Token{p.argi, TokenArg, parts[1]})
		}
		return token
	}

	if strings.HasPrefix(arg, "-") {
		if len(arg) == 1 {
			return &Token{Index: p.argi, Type: TokenShort}
		}
		short := arg[1:2]
		flag, ok := p.flags.short[short]
		// Not a known short flag, we'll just return it anyway.
		if !ok {
		} else if fb, ok := flag.value.(boolFlag); ok && fb.IsBoolFlag() {
			// Bool short flag.
		} else {
			// Short flag with combined argument: -fARG
			token := &Token{p.argi, TokenShort, short}
			if len(arg) > 2 {
				p.push(&Token{p.argi, TokenArg, arg[2:]})
			}
			return token
		}

		if len(arg) > 2 {
			p.args = append([]string{"-" + arg[2:]}, p.args...)
		}
		return &Token{p.argi, TokenShort, short}
	}

	return &Token{p.argi, TokenArg, arg}
}

func (p *ParseContext) Peek() *Token {
	if len(p.peek) == 0 {
		return p.push(p.Next())
	}
	return p.peek[len(p.peek)-1]
}

func (p *ParseContext) push(token *Token) *Token {
	p.peek = append(p.peek, token)
	return token
}

func (p *ParseContext) pop() *Token {
	end := len(p.peek) - 1
	token := p.peek[end]
	p.peek = p.peek[0:end]
	return token
}

func (p *ParseContext) String() string {
	return p.SelectedCommand
}

func (p *ParseContext) matchedFlag(flag *FlagClause, value string) {
	p.Elements = append(p.Elements, &ParseElement{Clause: flag, Value: &value})
}

func (p *ParseContext) matchedArg(arg *ArgClause, value string) {
	p.Elements = append(p.Elements, &ParseElement{Clause: arg, Value: &value})
}

func (p *ParseContext) matchedCmd(cmd *CmdClause) {
	p.Elements = append(p.Elements, &ParseElement{Clause: cmd})
	p.mergeFlags(cmd.flagGroup)
	p.mergeArgs(cmd.argGroup)
	p.SelectedCommand = cmd.name
}

// ExpandArgsFromFiles expands arguments in the form @<file> into one-arg-per-
// line read from that file.
func ExpandArgsFromFiles(args []string) ([]string, error) {
	out := []string{}
	for _, arg := range args {
		if strings.HasPrefix(arg, "@") {
			r, err := os.Open(arg[1:])
			if err != nil {
				return nil, err
			}
			scanner := bufio.NewScanner(r)
			for scanner.Scan() {
				out = append(out, scanner.Text())
			}
			r.Close()
			if scanner.Err() != nil {
				return nil, scanner.Err()
			}
		} else {
			out = append(out, arg)
		}
	}
	return out, nil
}

func parse(context *ParseContext, app *Application) (selected []string, err error) {
	context.mergeFlags(app.flagGroup)
	context.mergeArgs(app.argGroup)

	cmds := app.cmdGroup

loop:
	for !context.EOL() {
		token := context.Peek()
		switch token.Type {
		case TokenLong, TokenShort:
			if err := context.flags.parse(context); err != nil {
				return nil, err
			}

		case TokenArg:
			if cmds.have() {
				cmd, ok := cmds.commands[token.String()]
				if !ok {
					return nil, fmt.Errorf("expected command but got %s", token)
				}
				context.matchedCmd(cmd)
				selected = append([]string{token.String()}, selected...)
				cmds = cmd.cmdGroup
				context.Next()
			} else if context.arguments.have() {
				arg := context.nextArg()
				if arg == nil {
					break loop
				}
				context.matchedArg(arg, token.String())
				context.Next()
			} else {
				break loop
			}

		case TokenEOL:
			break loop
		}
	}

	if !context.EOL() {
		return nil, fmt.Errorf("unexpected %s", context.Peek())
	}

	// Set defaults for all remaining args.
	for arg := context.nextArg(); arg != nil && !arg.consumesRemainder(); arg = context.nextArg() {
		if arg.defaultValue != "" {
			if err := arg.value.Set(arg.defaultValue); err != nil {
				return nil, fmt.Errorf("invalid default value '%s' for argument '%s'", arg.defaultValue, arg.name)
			}
		}
	}

	return
}
