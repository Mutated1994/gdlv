// Copyright 2016, Gdlv Authors

package main

import (
	"bytes"
	"errors"
	"fmt"
	"go/parser"
	"go/scanner"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode"

	"golang.org/x/mobile/event/key"

	"github.com/aarzilli/gdlv/internal/dlvclient/service/api"

	"github.com/aarzilli/nucular"
	"github.com/aarzilli/nucular/label"
	"github.com/aarzilli/nucular/rect"
)

const optimizedFunctionWarning = "Warning: debugging optimized function"

type cmdfunc func(out io.Writer, args string) error

type command struct {
	aliases  []string
	complete func()
	helpMsg  string
	cmdFn    cmdfunc
}

// Returns true if the command string matches one of the aliases for this command
func (c command) match(cmdstr string) bool {
	for _, v := range c.aliases {
		if v == cmdstr {
			return true
		}
	}
	return false
}

type Commands struct {
	cmds    []command
	lastCmd cmdfunc
}

var (
	LongLoadConfig      = api.LoadConfig{true, 1, 64, 16, -1}
	LongArrayLoadConfig = api.LoadConfig{true, 1, 64, 64, -1}
	ShortLoadConfig     = api.LoadConfig{false, 0, 64, 0, 3}
)

type ByFirstAlias []command

func (a ByFirstAlias) Len() int           { return len(a) }
func (a ByFirstAlias) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByFirstAlias) Less(i, j int) bool { return a[i].aliases[0] < a[j].aliases[0] }

var cmdhistory = []string{""}
var historyShown int = 0
var historySearch bool
var historyNeedle string
var cmds *Commands

func DebugCommands() *Commands {
	c := &Commands{}

	c.cmds = []command{
		{aliases: []string{"help", "h"}, cmdFn: c.help, helpMsg: `Prints the help message.

	help [command]
	
Type "help" followed by the name of a command for more information about it.`},
		{aliases: []string{"break", "b"}, cmdFn: breakpoint, complete: completeLocation, helpMsg: `Sets a breakpoint.

	break [name] <linespec>

See $GOPATH/src/github.com/derekparker/delve/Documentation/cli/locspec.md for the syntax of linespec. To set breakpoints you can also right click on a source line and click "Set breakpoint". Breakpoint properties can be changed by right clicking on a breakpoint (either in the source panel or the breakpoints panel) and selecting "Edit breakpoint".`},
		{aliases: []string{"trace", "t"}, cmdFn: tracepoint, complete: completeLocation, helpMsg: `Set tracepoint.

	trace [name] <linespec>
	
A tracepoint is a breakpoint that does not stop the execution of the program, instead when the tracepoint is hit a notification is displayed. See $GOPATH/src/github.com/derekparker/delve/Documentation/cli/locspec.md for the syntax of linespec.

See also: "help on", "help cond" and "help clear"`},
		{aliases: []string{"clear"}, cmdFn: clear, helpMsg: `Deletes breakpoint.
		
			clear <breakpoint name or id>`},
		{aliases: []string{"restart", "r"}, cmdFn: restart, helpMsg: `Restart process.

For recordings a checkpoint can be optionally specified.
For live processes any argument to restart will be used as argument to the program, use:

	restart --
	
To clear the arguments passed to the program.`},
		{aliases: []string{"continue", "c"}, cmdFn: cont, helpMsg: "Run until breakpoint or program termination."},
		{aliases: []string{"rewind", "rw"}, cmdFn: rewind, helpMsg: "Run backwards until breakpoint or program termination."},
		{aliases: []string{"checkpoint", "check"}, cmdFn: checkpoint, helpMsg: `Creates a checkpoint at the current position.
	
	checkpoint [where]`},
		{aliases: []string{"step", "s"}, cmdFn: step, helpMsg: `Single step through program.
		
		step [-list|-first|-last|name]
		
Specify a name to step into one specific function call. Use the -list option for all the function calls on the current line. To step into a specific function call you can also right click on a function call (on the current line) and select "Step into".

Option -first will step into the first function call of the line, -last will step into the last call of the line. When called without arguments step will use -first as default, but this can be changed using config.`},
		{aliases: []string{"step-instruction", "si"}, cmdFn: stepInstruction, helpMsg: "Single step a single cpu instruction."},
		{aliases: []string{"next", "n"}, cmdFn: next, helpMsg: "Step over to next source line."},
		{aliases: []string{"stepout", "o"}, cmdFn: stepout, helpMsg: "Step out of the current function."},
		{aliases: []string{"cancelnext"}, cmdFn: cancelnext, helpMsg: "Cancels the next operation currently in progress."},
		{aliases: []string{"interrupt"}, cmdFn: interrupt, helpMsg: "interrupts execution."},
		{aliases: []string{"print", "p"}, complete: completeVariable, cmdFn: printVar, helpMsg: `Evaluate an expression.

	print <expression>

See $GOPATH/src/github.com/derekparker/delve/Documentation/cli/expr.md for a description of supported expressions.`},
		{aliases: []string{"list", "ls"}, complete: completeLocation, cmdFn: listCommand, helpMsg: `Show source code.
		
			list <linespec>
		
		See $GOPATH/src/github.com/derekparker/delve/Documentation/cli/expr.md for a description of supported expressions.`},
		{aliases: []string{"set"}, cmdFn: setVar, complete: completeVariable, helpMsg: `Changes the value of a variable.

	set <variable> = <value>

See $GOPATH/src/github.com/derekparker/delve/Documentation/cli/expr.md for a description of supported expressions. Only numerical variables and pointers can be changed.`},
		{aliases: []string{"display", "disp", "dp"}, complete: completeVariable, cmdFn: displayVar, helpMsg: `Adds one expression to the Variables panel.`},
		{aliases: []string{"layout"}, cmdFn: layoutCommand, helpMsg: `Manages window layout.
	
	layout <name>

Loads the specified layout.

	layout save <name> <descr>
	
Saves the current layout.

	layout list
	
Lists saved layouts.`},
		{aliases: []string{"config"}, cmdFn: configCommand, helpMsg: `Configuration`},
		{aliases: []string{"scroll"}, cmdFn: scrollCommand, helpMsg: `Controls scrollback behavior.
	
	scroll clear		Clears scrollback
	scroll silence		Silences output from inferior
	scroll noise		Re-enables output from inferior.
`},
		{aliases: []string{"exit", "quit", "q"}, cmdFn: exitCommand, helpMsg: "Exit the debugger."},

		{aliases: []string{"window", "win"}, complete: completeWindow, cmdFn: windowCommand, helpMsg: `Opens a window.
	
	window <kind>
	
Kind is one of listing, diassembly, goroutines, stacktrace, variables, globals, breakpoints, threads, registers, sources, functions, types and checkpoints.

Shortcuts:
	Alt-1	Listing window
	Alt-2	Variables window
	Alt-3	Globals window
	Alt-4	Registers window
	Alt-5	Breakpoints window
	Alt-6	Stacktrace window
	Alt-7	Disassembly window
	Alt-8	Goroutines window
	Alt-9	Threads Window
`},
	}

	sort.Sort(ByFirstAlias(c.cmds))
	return c
}

var noCmdError = errors.New("command not available")

func noCmdAvailable(out io.Writer, args string) error {
	return noCmdError
}

func nullCommand(out io.Writer, args string) error {
	return nil
}

func (c *Commands) help(out io.Writer, args string) error {
	if args != "" {
		for _, cmd := range c.cmds {
			for _, alias := range cmd.aliases {
				if alias == args {
					fmt.Fprintln(out, cmd.helpMsg)
					return nil
				}
			}
		}
		return noCmdError
	}

	fmt.Fprintln(out, "The following commands are available:")
	w := new(tabwriter.Writer)
	w.Init(out, 0, 8, 0, ' ', 0)
	for _, cmd := range c.cmds {
		h := cmd.helpMsg
		if idx := strings.Index(h, "\n"); idx >= 0 {
			h = h[:idx]
		}
		if len(cmd.aliases) > 1 {
			fmt.Fprintf(w, "    %s (alias: %s) \t %s\n", cmd.aliases[0], strings.Join(cmd.aliases[1:], " | "), h)
		} else {
			fmt.Fprintf(w, "    %s \t %s\n", cmd.aliases[0], h)
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(out, "Type help followed by a command for full documentation.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Keybindings:")
	fmt.Fprintln(w, "    Ctrl +/- \t Zoom in/out")
	fmt.Fprintln(w, "    Escape \t Focus command line")
	fmt.Fprintln(w, "    Ctrl delete \t Request manual stop")
	fmt.Fprintln(w, "    F5 \t Continue")
	fmt.Fprintln(w, "    Shift-F5 \t Request manual stop")
	fmt.Fprintln(w, "    F10 \t Next")
	fmt.Fprintln(w, "    F11 \t Step")
	fmt.Fprintln(w, "    Shift-F11 \t Step Out")
	if err := w.Flush(); err != nil {
		return err
	}
	return nil
}

func setBreakpoint(out io.Writer, tracepoint bool, argstr string) error {
	if curThread < 0 {
		cmd := "B"
		if tracepoint {
			cmd = "T"
		}
		ScheduledBreakpoints = append(ScheduledBreakpoints, fmt.Sprintf("%s%s", cmd, argstr))
		fmt.Fprintf(out, "Breakpoint will be set on restart\n")
		return nil
	}

	defer refreshState(refreshToSameFrame, clearBreakpoint, nil)
	args := strings.SplitN(argstr, " ", 2)

	requestedBp := &api.Breakpoint{}
	locspec := ""
	switch len(args) {
	case 1:
		locspec = argstr
	case 2:
		if api.ValidBreakpointName(args[0]) == nil {
			requestedBp.Name = args[0]
			locspec = args[1]
		} else {
			locspec = argstr
		}
	default:
		return fmt.Errorf("address required")
	}

	requestedBp.Tracepoint = tracepoint
	locs, err := client.FindLocation(api.EvalScope{curGid, curFrame}, locspec)
	if err != nil {
		if requestedBp.Name == "" {
			return err
		}
		requestedBp.Name = ""
		locspec = argstr
		var err2 error
		locs, err2 = client.FindLocation(api.EvalScope{curGid, curFrame}, locspec)
		if err2 != nil {
			return err
		}
	}
	for _, loc := range locs {
		requestedBp.Addr = loc.PC
		setBreakpointEx(out, requestedBp)
	}
	return nil
}

func setBreakpointEx(out io.Writer, requestedBp *api.Breakpoint) {
	if curThread < 0 {
		switch {
		default:
			fallthrough
		case requestedBp.Addr != 0:
			fmt.Fprintf(out, "error: process exited\n")
			return
		case requestedBp.FunctionName != "":
			ScheduledBreakpoints = append(ScheduledBreakpoints, fmt.Sprintf("B%s", requestedBp.FunctionName))
		case requestedBp.File != "":
			ScheduledBreakpoints = append(ScheduledBreakpoints, fmt.Sprintf("T%s:%d", requestedBp.File, requestedBp.Line))
		}
		fmt.Fprintf(out, "Breakpoint will be set on restart\n")
		return
	}
	bp, err := client.CreateBreakpoint(requestedBp)
	if err != nil {
		fmt.Fprintf(out, "Could not create breakpoint: %v\n", err)
	}

	fmt.Fprintf(out, "%s set at %s\n", formatBreakpointName(bp, true), formatBreakpointLocation(bp))
	freezeBreakpoint(out, bp)
}

func breakpoint(out io.Writer, args string) error {
	return setBreakpoint(out, false, args)
}

func tracepoint(out io.Writer, args string) error {
	return setBreakpoint(out, true, args)
}

func clear(out io.Writer, args string) error {
	if len(args) == 0 {
		return fmt.Errorf("not enough arguments")
	}
	id, err := strconv.Atoi(args)
	var bp *api.Breakpoint
	if err == nil {
		bp, err = client.ClearBreakpoint(id)
	} else {
		bp, err = client.ClearBreakpointByName(args)
	}
	removeFrozenBreakpoint(bp)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s cleared at %s\n", formatBreakpointName(bp, true), formatBreakpointLocation(bp))
	return nil
}

func restart(out io.Writer, args string) error {
	if client != nil && client.Recorded() {
		_, err := client.RestartFrom(args, false, nil)
		refreshState(refreshToFrameZero, clearStop, nil)
		return err
	}

	resetArgs := false
	var newArgs []string
	args = strings.TrimSpace(args)
	if args != "" {
		argv := splitQuotedFields(args)
		if len(argv) > 0 {
			if argv[0] == "--" {
				argv = argv[1:]
			}
			resetArgs = true
			newArgs = argv
		}
	}

	if BackendServer.StaleExecutable() {
		wnd.PopupOpen("Recompile?", dynamicPopupFlags, rect.Rect{100, 100, 550, 400}, true, func(w *nucular.Window) {
			w.Row(30).Static(0)
			w.Label("Executable is stale. Rebuild?", "LC")
			var yes, no bool
			for _, e := range w.Input().Keyboard.Keys {
				switch {
				case e.Code == key.CodeEscape:
					no = true
				case e.Code == key.CodeReturnEnter:
					yes = true
				}
			}
			w.Row(30).Static(0, 100, 100, 0)
			w.Spacing(1)
			if w.ButtonText("Yes") {
				yes = true
			}
			if w.ButtonText("No") {
				no = true
			}
			w.Spacing(1)

			switch {
			case yes:
				go pseudoCommandWrap(doRebuild)
				w.Close()
			case no:
				go pseudoCommandWrap(func(w io.Writer) error {
					return doRestart(w, resetArgs, newArgs)
				})
				w.Close()
			}
		})
		return nil
	}

	return doRestart(out, resetArgs, newArgs)
}

func splitQuotedFields(in string) []string {
	type stateEnum int
	const (
		inSpace stateEnum = iota
		inField
		inQuote
		inQuoteEscaped
	)
	state := inSpace
	r := []string{}
	var buf bytes.Buffer

	for _, ch := range in {
		switch state {
		case inSpace:
			if ch == '\'' {
				state = inQuote
			} else if !unicode.IsSpace(ch) {
				buf.WriteRune(ch)
				state = inField
			}

		case inField:
			if ch == '\'' {
				state = inQuote
			} else if unicode.IsSpace(ch) {
				r = append(r, buf.String())
				buf.Reset()
			} else {
				buf.WriteRune(ch)
			}

		case inQuote:
			if ch == '\'' {
				state = inField
			} else if ch == '\\' {
				state = inQuoteEscaped
			} else {
				buf.WriteRune(ch)
			}

		case inQuoteEscaped:
			buf.WriteRune(ch)
			state = inQuote
		}
	}

	if buf.Len() != 0 {
		r = append(r, buf.String())
	}

	return r
}

func pseudoCommandWrap(cmd func(io.Writer) error) {
	mu.Lock()
	running = true
	wnd.Changed()
	mu.Unlock()
	defer func() {
		mu.Lock()
		running = false
		wnd.Changed()
		mu.Unlock()
	}()

	out := editorWriter{&scrollbackEditor, true}
	err := cmd(&out)
	if err != nil {
		fmt.Fprintf(&out, "Error executing command: %v\n", err)
	}
}

func doRestart(out io.Writer, resetArgs bool, args []string) error {
	_, err := client.RestartFrom("", resetArgs, args)
	if err != nil {
		return err
	}
	finishRestart(out, true)
	refreshState(refreshToFrameZero, clearStop, nil)
	return nil
}

func doRebuild(out io.Writer) error {
	dorestart := BackendServer.serverProcess != nil
	BackendServer.Rebuild()
	if !dorestart || !BackendServer.buildok {
		return nil
	}

	updateFrozenBreakpoints()
	clearFrozenBreakpoints()

	discarded, err := client.Restart()
	if err != nil {
		fmt.Fprintf(out, "error on restart\n")
		return err
	}
	fmt.Fprintln(out, "Process restarted with PID", client.ProcessPid())
	for i := range discarded {
		fmt.Fprintf(out, "Discarded %s at %s: %v\n", formatBreakpointName(discarded[i].Breakpoint, false), formatBreakpointLocation(discarded[i].Breakpoint), discarded[i].Reason)
	}

	restoreFrozenBreakpoints(out)

	finishRestart(out, true)

	refreshState(refreshToFrameZero, clearStop, nil)
	return nil
}

func cont(out io.Writer, args string) error {
	stateChan := client.Continue()
	var state *api.DebuggerState
	for state = range stateChan {
		if state.Err != nil {
			refreshState(refreshToFrameZero, clearStop, state)
			return state.Err
		}
		printcontext(out, state)
	}
	refreshState(refreshToFrameZero, clearStop, state)
	return nil
}

func rewind(out io.Writer, args string) error {
	stateChan := client.Rewind()
	var state *api.DebuggerState
	for state = range stateChan {
		if state.Err != nil {
			refreshState(refreshToFrameZero, clearStop, state)
			return state.Err
		}
		printcontext(out, state)
	}
	refreshState(refreshToFrameZero, clearStop, state)
	return nil
}

func continueUntilCompleteNext(out io.Writer, state *api.DebuggerState, op string, bp *api.Breakpoint) error {
	if !state.NextInProgress {
		refreshState(refreshToFrameZero, clearStop, state)
		return nil
	}
	for {
		stateChan := client.Continue()
		var state *api.DebuggerState
		for state = range stateChan {
			if state.Err != nil {
				refreshState(refreshToFrameZero, clearStop, state)
				return state.Err
			}
			printcontext(out, state)
		}
		if bp != nil {
			for _, th := range state.Threads {
				if th.Breakpoint != nil && th.Breakpoint.ID == bp.ID {
					refreshState(refreshToFrameZero, clearStop, state)
					return nil
				}
			}
		}
		if !state.NextInProgress || conf.StopOnNextBreakpoint {
			refreshState(refreshToFrameZero, clearStop, state)
			return nil
		}
		fmt.Fprintf(out, "    breakpoint hit during %s, continuing...\n", op)
	}
}

func step(out io.Writer, args string) error {
	getsics := func() ([]stepIntoCall, uint64, error) {
		state, err := client.GetState()
		if err != nil {
			return nil, 0, err
		}
		if curGid < 0 {
			return nil, 0, errors.New("no selected goroutine")
		}
		loc := currentLocation(state)
		if loc == nil {
			return nil, 0, errors.New("could not find current location")
		}
		return stepIntoList(*loc), state.CurrentThread.PC, nil
	}

	if args == "" {
		args = conf.DefaultStepBehaviour
	}

	switch args {
	case "", "-first":
		return stepIntoFirst(out)

	case "-last":
		sics, _, _ := getsics()
		if len(sics) > 0 {
			return stepInto(out, sics[len(sics)-1])
		} else {
			return stepIntoFirst(out)
		}

	case "-list":
		sics, pc, err := getsics()
		if err != nil {
			return err
		}
		for _, sic := range sics {
			if sic.Inst.Loc.PC >= pc {
				fmt.Fprintf(out, "%s\t%s\n", sic.Name, sic.ExprString())
			}
		}
	default:
		sics, _, err := getsics()
		if err != nil {
			return err
		}
		for _, sic := range sics {
			if sic.Name == args {
				return stepInto(out, sic)
			}
		}
		return fmt.Errorf("could not find call %s", args)
	}
	return nil
}

func stepIntoFirst(out io.Writer) error {
	state, err := client.Step()
	if err != nil {
		return err
	}
	printcontext(out, state)
	return continueUntilCompleteNext(out, state, "step", nil)
}

func stepInto(out io.Writer, sic stepIntoCall) error {
	stack, err := client.Stacktrace(curGid, 1, nil)
	if err != nil {
		return err
	}
	if len(stack) < 1 {
		return errors.New("could not stacktrace")
	}
	cond := fmt.Sprintf("(runtime.curg.goid == %d) && (runtime.frameoff == %d)", curGid, stack[0].FrameOffset)
	bp, err := client.CreateBreakpoint(&api.Breakpoint{Addr: sic.Inst.Loc.PC, Cond: cond})
	if err != nil {
		return err
	}

	// we use next here instead of continue so that if for any reason the
	// breakpoint can not be reached (for example a panic or a branch) we will
	// not run the program to completion
	state, err := client.Next()
	if err != nil {
		client.ClearBreakpoint(bp.ID)
		return err
	}
	printcontext(out, state)
	err = continueUntilCompleteNext(out, state, "step", nil)
	client.ClearBreakpoint(bp.ID)
	if err != nil {
		return err
	}
	bpfound := false
	for _, th := range state.Threads {
		if th.Breakpoint != nil && th.Breakpoint.ID == bp.ID {
			bpfound = true
			break
		}
	}
	if bpfound {
		return stepIntoFirst(out)
	}
	return nil
}

func stepInstruction(out io.Writer, args string) error {
	state, err := client.StepInstruction()
	if err != nil {
		return err
	}
	printcontext(out, state)
	refreshState(refreshToFrameZero, clearStop, state)
	return nil
}

func next(out io.Writer, args string) error {
	state, err := client.Next()
	if err != nil {
		return err
	}
	printcontext(out, state)
	return continueUntilCompleteNext(out, state, "next", nil)
}

func stepout(out io.Writer, args string) error {
	state, err := client.StepOut()
	if err != nil {
		return err
	}
	printcontext(out, state)
	return continueUntilCompleteNext(out, state, "stepout", nil)
}

func cancelnext(out io.Writer, args string) error {
	return client.CancelNext()
}

func interrupt(out io.Writer, args string) error {
	_, err := client.Halt()
	if err != nil {
		return err
	}
	//refreshState(refreshToFrameZero, clearStop, state)
	return nil
}

func printVar(out io.Writer, args string) error {
	if len(args) == 0 {
		return fmt.Errorf("not enough arguments")
	}
	val, err := client.EvalVariable(api.EvalScope{curGid, curFrame}, args, getVariableLoadConfig())
	if err != nil {
		return err
	}
	valstr := val.MultilineString("")
	nlcount := 0
	for _, ch := range valstr {
		if ch == '\n' {
			nlcount++
		}
	}
	if nlcount > 20 {
		fmt.Fprintln(out, "Expression added to variables panel")
		addExpression(args)
	} else {
		fmt.Fprintln(out, valstr)
	}
	return nil
}

func displayVar(out io.Writer, args string) error {
	addExpression(args)
	return nil
}

func listCommand(out io.Writer, args string) error {
	locs, err := client.FindLocation(api.EvalScope{curGid, curFrame}, args)
	if err != nil {
		return err
	}
	switch len(locs) {
	case 1:
		// ok
	case 0:
		return errors.New("no location found")
	default:
		return errors.New("can not list multiple locations")
	}

	listingPanel.pinnedLoc = &locs[0]
	refreshState(refreshToSameFrame, clearNothing, nil)

	return nil
}

func setVar(out io.Writer, args string) error {
	// HACK: in go '=' is not an operator, we detect the error and try to recover from it by splitting the input string
	_, err := parser.ParseExpr(args)
	if err == nil {
		return fmt.Errorf("syntax error '=' not found")
	}

	el, ok := err.(scanner.ErrorList)
	if !ok || el[0].Msg != "expected '==', found '='" {
		return err
	}

	lexpr := args[:el[0].Pos.Offset]
	rexpr := args[el[0].Pos.Offset+1:]
	return client.SetVariable(api.EvalScope{curGid, curFrame}, lexpr, rexpr)
}

// ExitRequestError is returned when the user
// exits Delve.
type ExitRequestError struct{}

func (ere ExitRequestError) Error() string {
	return ""
}

func exitCommand(out io.Writer, args string) error {
	return ExitRequestError{}
}

func checkpoint(out io.Writer, args string) error {
	if args == "" {
		state, err := client.GetState()
		if err != nil {
			return err
		}
		var loc api.Location = api.Location{PC: state.CurrentThread.PC, File: state.CurrentThread.File, Line: state.CurrentThread.Line, Function: state.CurrentThread.Function}
		if state.SelectedGoroutine != nil {
			loc = state.SelectedGoroutine.CurrentLoc
		}
		fname := "???"
		if loc.Function != nil {
			fname = loc.Function.Name
		}
		args = fmt.Sprintf("%s() %s:%d (%#x)", fname, loc.File, loc.Line, loc.PC)
	}

	cpid, err := client.Checkpoint(args)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "Checkpoint c%d created.\n", cpid)
	refreshState(refreshToSameFrame, clearBreakpoint, nil)
	return nil
}

func layoutCommand(out io.Writer, args string) error {
	argv := strings.SplitN(args, " ", 3)
	if len(argv) < 0 {
		return fmt.Errorf("not enough arguments")
	}
	switch argv[0] {
	case "list":
		w := new(tabwriter.Writer)
		w.Init(out, 0, 8, 0, ' ', 0)
		for name, ld := range conf.Layouts {
			fmt.Fprintf(w, "%s \t %s\n", name, ld.Description)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	case "save":
		if len(argv) < 2 {
			return fmt.Errorf("not enough arguments")
		}
		name := argv[1]
		description := ""
		if len(argv) > 2 {
			description = argv[2]
		}

		conf.Layouts[name] = LayoutDescr{Description: description, Layout: serializeLayout()}
		saveConfiguration()
	default:
		ld, ok := conf.Layouts[argv[0]]
		if !ok {
			return fmt.Errorf("unknown layout %q", argv[0])
		}
		loadPanelDescrToplevel(ld.Layout)
		wnd.Changed()
	}
	return nil
}

func configCommand(out io.Writer, args string) error {
	cw := newConfigWindow()
	wnd.PopupOpen("Configuration", dynamicPopupFlags, rect.Rect{100, 100, 600, 700}, true, cw.Update)
	return nil
}

type configWindow struct {
	selectedSubstitutionRule int
	from                     nucular.TextEditor
	to                       nucular.TextEditor
}

func newConfigWindow() *configWindow {
	return &configWindow{
		selectedSubstitutionRule: -1,
		from:                     nucular.TextEditor{Flags: nucular.EditSelectable | nucular.EditClipboard},
		to:                       nucular.TextEditor{Flags: nucular.EditSelectable | nucular.EditClipboard},
	}
}

func (cw *configWindow) Update(w *nucular.Window) {
	const col1 = 160
	w.Row(20).Static(col1, 200)
	w.Label("Theme:", "LC")
	if conf.Theme == "" {
		conf.Theme = darkTheme
	}
	if w := w.Combo(label.TA(conf.Theme, "LC"), 100, nil); w != nil {
		w.Row(20).Dynamic(1)
		for _, theme := range themes {
			if w.MenuItem(label.TA(theme, "LC")) {
				conf.Theme = theme
				setupStyle()
			}
		}
	}

	w.Row(20).Static(col1, 150)
	w.Label("Disassembly Flavor:", "LC")
	disassfl := []string{"Intel", "GNU"}
	conf.DisassemblyFlavour = w.ComboSimple(disassfl, conf.DisassemblyFlavour, 20)

	w.Row(20).Dynamic(1)
	w.Label("When a breakpoint is hit during next/step/stepout gdlv should:", "LC")
	w.Row(20).Static(col1, 200)
	w.Spacing(1)
	breakb := []string{"Automatically continue", "Stop"}
	breakbLbl := breakb[0]
	if conf.StopOnNextBreakpoint {
		breakbLbl = breakb[1]
	}
	if w := w.Combo(label.TA(breakbLbl, "LC"), 100, nil); w != nil {
		w.Row(20).Dynamic(1)
		if w.MenuItem(label.TA(breakb[0], "LC")) {
			conf.StopOnNextBreakpoint = false
		}
		if w.MenuItem(label.TA(breakb[1], "LC")) {
			conf.StopOnNextBreakpoint = true
		}
	}

	w.Row(20).Static()
	w.LayoutFitWidth(0, 100)
	w.Label("Default step behavior:", "LC")
	w.LayoutSetWidth(200)
	stepBehaviours := []string{"-first", "-last"}
	stepBehaviourIdx := 0
	for i := range stepBehaviours {
		if conf.DefaultStepBehaviour == stepBehaviours[i] {
			stepBehaviourIdx = i
			break
		}
	}
	i := w.ComboSimple(stepBehaviours, stepBehaviourIdx, 20)
	if i >= 0 {
		conf.DefaultStepBehaviour = stepBehaviours[i]
	}

	if conf.MaxArrayValues == 0 {
		conf.MaxArrayValues = LongLoadConfig.MaxArrayValues
	}
	if conf.MaxStringLen == 0 {
		conf.MaxStringLen = LongLoadConfig.MaxStringLen
	}

	w.Row(30).Static(0)

	w.Row(30).Static(200, 200)
	w.Label("Load configuration:", "LC")
	w.PropertyInt("Max array load:", 1, &conf.MaxArrayValues, 4096, 1, 1)
	w.Row(30).Static(200, 200)
	w.Spacing(1)
	w.PropertyInt("Max string load:", 1, &conf.MaxStringLen, 4096, 1, 1)

	w.Row(30).Static(0)

	w.Row(30).Static(0)
	w.Label("Path substitutions:", "LC")
	w.Row(240).Static(0, 100)
	if w := w.GroupBegin("path-substitution-list", nucular.WindowNoHScrollbar); w != nil {
		w.Row(30).Static(0)
		if len(conf.SubstitutePath) == 0 {
			w.Label("(no substitution rules)", "LC")
		}
		for i, r := range conf.SubstitutePath {
			s := cw.selectedSubstitutionRule == i
			w.SelectableLabel(fmt.Sprintf("%s -> %s", r.From, r.To), "LC", &s)
			if s {
				cw.selectedSubstitutionRule = i
			}
		}
		w.GroupEnd()
	}
	if w := w.GroupBegin("path-substitution-controls", nucular.WindowNoScrollbar); w != nil {
		w.Row(30).Static(0)
		if w.ButtonText("Remove") && cw.selectedSubstitutionRule >= 0 && cw.selectedSubstitutionRule < len(conf.SubstitutePath) {
			copy(conf.SubstitutePath[cw.selectedSubstitutionRule:], conf.SubstitutePath[cw.selectedSubstitutionRule+1:])
			conf.SubstitutePath = conf.SubstitutePath[:len(conf.SubstitutePath)-1]
			cw.selectedSubstitutionRule = -1
		}
		w.GroupEnd()
	}

	w.Row(30).Static(0)
	w.Label("New rule:", "LC")
	w.Row(30).Static(50, 150, 50, 150, 80)
	w.Label("From:", "LC")
	cw.from.Edit(w)
	w.Label("To:", "LC")
	cw.to.Edit(w)
	if w.ButtonText("Add") {
		conf.SubstitutePath = append(conf.SubstitutePath, SubstitutePathRule{From: string(cw.from.Buffer), To: string(cw.to.Buffer)})
		cw.from.Buffer = cw.from.Buffer[:0]
		cw.to.Buffer = cw.to.Buffer[:0]
	}

	w.Row(30).Static(0)

	w.Row(20).Static(0, 100)
	w.Spacing(1)
	if w.ButtonText("OK") {
		saveConfiguration()
		w.Close()
	}
}

func scrollCommand(out io.Writer, args string) error {
	switch args {
	case "clear":
		mu.Lock()
		scrollbackEditor.Buffer = scrollbackEditor.Buffer[:0]
		scrollbackEditor.Cursor = 0
		scrollbackEditor.CursorFollow = true
		mu.Unlock()
	case "silence":
		mu.Lock()
		silenced = true
		mu.Unlock()
		fmt.Fprintf(out, "Inferior output silenced\n")
	case "noise":
		mu.Lock()
		silenced = false
		mu.Unlock()
		fmt.Fprintf(out, "Inferior output enabled\n")
	default:
		mu.Lock()
		s := silenced
		mu.Unlock()
		if s {
			fmt.Fprintf(out, "Inferior output is silenced\n")
		} else {
			fmt.Fprintf(out, "Inferior output is not silenced\n")
		}
	}
	return nil
}

func windowCommand(out io.Writer, args string) error {
	args = strings.ToLower(strings.TrimSpace(args))
	foundw := ""
	for _, w := range infoModes {
		if strings.ToLower(w) == args {
			openWindow(w)
			return nil
		}
		if strings.HasPrefix(strings.ToLower(w), args) {
			if foundw != "" {
				return fmt.Errorf("unknown window kind %q", args)
			}
			foundw = w
		}
	}
	if foundw != "" {
		openWindow(foundw)
		return nil
	}
	return fmt.Errorf("unknown window kind %q", args)
}

func formatBreakpointName(bp *api.Breakpoint, upcase bool) string {
	thing := "breakpoint"
	if bp.Tracepoint {
		thing = "tracepoint"
	}
	if upcase {
		thing = strings.Title(thing)
	}
	id := bp.Name
	if id == "" {
		id = strconv.Itoa(bp.ID)
	}
	return fmt.Sprintf("%s %s", thing, id)
}

func formatBreakpointLocation(bp *api.Breakpoint) string {
	p := ShortenFilePath(bp.File)
	if bp.FunctionName != "" {
		return fmt.Sprintf("%#v for %s() %s:%d", bp.Addr, bp.FunctionName, p, bp.Line)
	}
	return fmt.Sprintf("%#v for %s:%d", bp.Addr, p, bp.Line)
}

func printcontext(out io.Writer, state *api.DebuggerState) error {
	for i := range state.Threads {
		if (state.CurrentThread != nil) && (state.Threads[i].ID == state.CurrentThread.ID) {
			continue
		}
		if state.Threads[i].Breakpoint != nil {
			printcontextThread(out, state.Threads[i])
		}
	}

	if state.CurrentThread == nil {
		fmt.Fprintln(out, "No current thread available")
		return nil
	}
	if len(state.CurrentThread.File) == 0 {
		fmt.Fprintf(out, "Stopped at: 0x%x\n", state.CurrentThread.PC)
		return nil
	}

	printcontextThread(out, state.CurrentThread)

	return nil
}

func printReturnValues(out io.Writer, th *api.Thread) {
	if len(th.ReturnValues) == 0 {
		return
	}
	fmt.Fprintln(out, "Values returned:")
	for _, v := range th.ReturnValues {
		fmt.Fprintf(out, "\t%s: %s\n", v.Name, v.MultilineString("\t"))
	}
	fmt.Fprintln(out)
}

func printcontextThread(out io.Writer, th *api.Thread) {
	fn := th.Function

	if th.Breakpoint == nil {
		fmt.Fprintf(out, "> %s() %s:%d (PC: %#v)\n", fn.Name, ShortenFilePath(th.File), th.Line, th.PC)
		if th.Function != nil && th.Function.Optimized {
			fmt.Fprintln(out, optimizedFunctionWarning)
		}
		printReturnValues(out, th)
		return
	}

	args := ""
	if th.BreakpointInfo != nil && th.Breakpoint.LoadArgs != nil && *th.Breakpoint.LoadArgs == ShortLoadConfig {
		var arg []string
		for _, ar := range th.BreakpointInfo.Arguments {
			arg = append(arg, ar.SinglelineString())
		}
		args = strings.Join(arg, ", ")
	}

	bpname := ""
	if th.Breakpoint.Name != "" {
		bpname = fmt.Sprintf("[%s] ", th.Breakpoint.Name)
	}

	if hitCount, ok := th.Breakpoint.HitCount[strconv.Itoa(th.GoroutineID)]; ok {
		fmt.Fprintf(out, "> %s%s(%s) %s:%d (hits goroutine(%d):%d total:%d) (PC: %#v)\n",
			bpname,
			fn.Name,
			args,
			ShortenFilePath(th.File),
			th.Line,
			th.GoroutineID,
			hitCount,
			th.Breakpoint.TotalHitCount,
			th.PC)
	} else {
		fmt.Fprintf(out, "> %s%s(%s) %s:%d (hits total:%d) (PC: %#v)\n",
			bpname,
			fn.Name,
			args,
			ShortenFilePath(th.File),
			th.Line,
			th.Breakpoint.TotalHitCount,
			th.PC)
	}
	if th.Function != nil && th.Function.Optimized {
		fmt.Fprintln(out, optimizedFunctionWarning)
	}

	printReturnValues(out, th)

	if th.BreakpointInfo != nil {
		bp := th.Breakpoint
		bpi := th.BreakpointInfo

		if bpi.Goroutine != nil {
			writeGoroutineLong(os.Stdout, bpi.Goroutine, "\t")
		}

		for _, v := range bpi.Variables {
			fmt.Fprintf(out, "    %s: %s\n", v.Name, v.MultilineString("\t"))
		}

		for _, v := range bpi.Locals {
			if *bp.LoadLocals == LongLoadConfig {
				fmt.Fprintf(out, "    %s: %s\n", v.Name, v.MultilineString("\t"))
			} else {
				fmt.Fprintf(out, "    %s: %s\n", v.Name, v.SinglelineString())
			}
		}

		if bp.LoadArgs != nil && *bp.LoadArgs == LongLoadConfig {
			for _, v := range bpi.Arguments {
				fmt.Fprintf(out, "    %s: %s\n", v.Name, v.MultilineString("\t"))
			}
		}

		if bpi.Stacktrace != nil {
			fmt.Fprintf(out, "    Stack:\n")
			printStack(out, bpi.Stacktrace, "        ")
		}
	}
}

func formatLocation(loc api.Location) string {
	fname := ""
	if loc.Function != nil {
		fname = loc.Function.Name
	}
	return fmt.Sprintf("%s at %s:%d (%#v)", fname, ShortenFilePath(loc.File), loc.Line, loc.PC)
}

func writeGoroutineLong(w io.Writer, g *api.Goroutine, prefix string) {
	fmt.Fprintf(w, "%sGoroutine %d:\n%s\tRuntime: %s\n%s\tUser: %s\n%s\tGo: %s\n",
		prefix, g.ID,
		prefix, formatLocation(g.CurrentLoc),
		prefix, formatLocation(g.UserCurrentLoc),
		prefix, formatLocation(g.GoStatementLoc))
}

func printStack(out io.Writer, stack []api.Stackframe, ind string) {
	if len(stack) == 0 {
		return
	}
	d := digits(len(stack) - 1)
	fmtstr := "%s%" + strconv.Itoa(d) + "d  0x%016x in %s\n"
	s := ind + strings.Repeat(" ", d+2+len(ind))

	for i := range stack {
		name := "(nil)"
		if stack[i].Function != nil {
			name = stack[i].Function.Name
		}
		fmt.Fprintf(out, fmtstr, ind, i, stack[i].PC, name)
		fmt.Fprintf(out, "%sat %s:%d\n", s, ShortenFilePath(stack[i].File), stack[i].Line)

		for j := range stack[i].Arguments {
			fmt.Fprintf(out, "%s    %s = %s\n", s, stack[i].Arguments[j].Name, stack[i].Arguments[j].SinglelineString())
		}
		for j := range stack[i].Locals {
			fmt.Fprintf(out, "%s    %s = %s\n", s, stack[i].Locals[j].Name, stack[i].Locals[j].SinglelineString())
		}
	}
}

// ShortenFilePath take a full file path and attempts to shorten
// it by replacing the current directory to './'.
func ShortenFilePath(fullPath string) string {
	workingDir, _ := os.Getwd()
	return strings.Replace(fullPath, workingDir, ".", 1)
}

func executeCommand(cmdstr string) {
	mu.Lock()
	running = true
	wnd.Changed()
	mu.Unlock()
	defer func() {
		mu.Lock()
		running = false
		wnd.Changed()
		mu.Unlock()
	}()

	out := editorWriter{&scrollbackEditor, true}
	cmdstr, args := parseCommand(cmdstr)
	if err := cmds.Call(cmdstr, args, &out); err != nil {
		if _, ok := err.(ExitRequestError); ok {
			if client != nil && client.AttachedToExistingProcess() && curThread >= 0 {
				wnd.PopupOpen("Confirm Quit", dynamicPopupFlags, rect.Rect{100, 100, 400, 700}, true, confirmQuit)
				return
			} else {
				if client != nil {
					client.Detach(true)
				}
				wnd.Close()
			}
		}
		// The type information gets lost in serialization / de-serialization,
		// so we do a string compare on the error message to see if the process
		// has exited, or if the command actually failed.
		if strings.Contains(err.Error(), "exited") {
			fmt.Fprintln(&out, err.Error())
		} else {
			fmt.Fprintf(&out, "Command failed: %s\n", err)
		}
	}
}

func confirmQuit(w *nucular.Window) {
	w.Row(20).Dynamic(1)
	w.Label("Would you like to kill the process?", "LT")
	w.Row(20).Static(0, 80, 80, 0)
	w.Spacing(1)
	exit := false
	kill := false
	if w.ButtonText("Yes") {
		exit = true
		kill = true
	}
	if w.ButtonText("No") {
		exit = true
		kill = false
	}
	if exit {
		client.Detach(kill)
		go wnd.Close()
	}
	w.Spacing(1)
}

func parseCommand(cmdstr string) (string, string) {
	vals := strings.SplitN(strings.TrimSpace(cmdstr), " ", 2)
	if len(vals) == 1 {
		return vals[0], ""
	}
	return vals[0], strings.TrimSpace(vals[1])
}

// Find will look up the command function for the given command input.
// If it cannot find the command it will default to noCmdAvailable().
// If the command is an empty string it will replay the last command.
func (c *Commands) Find(cmdstr string) cmdfunc {
	// If <enter> use last command, if there was one.
	if cmdstr == "" {
		if c.lastCmd != nil {
			return c.lastCmd
		}
		return nullCommand
	}

	for _, v := range c.cmds {
		if v.match(cmdstr) {
			c.lastCmd = v.cmdFn
			return v.cmdFn
		}
	}

	return noCmdAvailable
}

func (c *Commands) Call(cmdstr, args string, out io.Writer) error {
	return c.Find(cmdstr)(out, args)
}

func doCommand(cmd string) {
	var scrollbackOut = editorWriter{&scrollbackEditor, false}
	fmt.Fprintf(&scrollbackOut, "%s %s\n", currentPrompt(), cmd)
	go executeCommand(cmd)
}

func continueToLine(file string, lineno int) {
	out := editorWriter{&scrollbackEditor, true}
	bp, err := client.CreateBreakpoint(&api.Breakpoint{File: file, Line: lineno})
	if err != nil {
		fmt.Fprintf(&out, "Could not continue to specified line, could not create breakpoint: %v\n", err)
		return
	}
	state, err := client.StepOut()
	if err != nil {
		fmt.Fprintf(&out, "Could not continue to specified line, could not step out: %v\n", err)
		return
	}
	printcontext(&out, state)
	err = continueUntilCompleteNext(&out, state, "continue-to-line", bp)
	client.ClearBreakpoint(bp.ID)
	client.CancelNext()
	refreshState(refreshToSameFrame, clearBreakpoint, nil)
	if err != nil {
		fmt.Fprintf(&out, "Could not continue to specified line, could not step out: %v\n", err)
		return
	}
}

func getVariableLoadConfig() api.LoadConfig {
	cfg := LongLoadConfig
	if conf.MaxArrayValues > 0 {
		cfg.MaxArrayValues = conf.MaxArrayValues
	}
	if conf.MaxStringLen > 0 {
		cfg.MaxStringLen = conf.MaxStringLen
	}
	return cfg
}
