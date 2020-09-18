package rpc2

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
	"sync"
	"time"

	"github.com/aarzilli/gdlv/internal/dlvclient/service/api"
)

// Client is a RPC service.Client.
type RPCClient struct {
	addr   string
	client *rpc.Client

	mu sync.Mutex

	running, recording bool

	retValLoadCfg *api.LoadConfig

	recordedCache *bool
}

// NewClient creates a new RPCClient.
func NewClient(addr string, logFile io.Writer) (*RPCClient, error) {
	netclient, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	var rwc io.ReadWriteCloser = netclient
	if logFile != nil {
		rwc = &LogClient{netclient, logFile}
	}
	client := jsonrpc.NewClient(rwc)
	c := &RPCClient{addr: addr, client: client}
	c.call("SetApiVersion", api.SetAPIVersionIn{2}, &api.SetAPIVersionOut{})
	return c, nil
}

func (c *RPCClient) Running() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running || c.recording
}

func (c *RPCClient) ProcessPid() int {
	out := new(ProcessPidOut)
	c.call("ProcessPid", ProcessPidIn{}, out)
	return out.Pid
}

func (c *RPCClient) LastModified() time.Time {
	out := new(LastModifiedOut)
	c.call("LastModified", LastModifiedIn{}, out)
	return out.Time
}

func (c *RPCClient) Detach(kill bool) error {
	defer c.client.Close()
	out := new(DetachOut)
	return c.call("Detach", DetachIn{kill}, out)
}

func (c *RPCClient) RestartFrom(pos string, resetArgs bool, newArgs []string, rerecord bool) ([]api.DiscardedBreakpoint, error) {
	out := new(RestartOut)
	err := c.call("Restart", RestartIn{pos, resetArgs, newArgs, rerecord}, out)
	return out.DiscardedBreakpoints, err
}

func (c *RPCClient) GetState() (*api.DebuggerState, error) {
	var out StateOut
	err := c.call("State", StateIn{}, &out)
	return out.State, err
}

func (c *RPCClient) Continue() <-chan *api.DebuggerState {
	return c.continueDir(api.Continue)
}

func (c *RPCClient) Rewind() <-chan *api.DebuggerState {
	return c.continueDir(api.Rewind)
}

type ProcessExitedError struct {
	pid, exitStatus int
}

func (err *ProcessExitedError) Error() string {
	return fmt.Sprintf("Process %d has exited with status %d", err.pid, err.exitStatus)
}

func (c *RPCClient) continueDir(cmd string) <-chan *api.DebuggerState {
	ch := make(chan *api.DebuggerState)
	go func() {
		for {
			out := new(CommandOut)
			err := c.call("Command", &api.DebuggerCommand{Name: cmd, ReturnInfoLoadConfig: c.retValLoadCfg}, &out)
			state := out.State
			if err != nil {
				state.Err = err
			}
			if state.Exited {
				// Error types apparantly cannot be marshalled by Go correctly. Must reset error here.
				state.Err = &ProcessExitedError{c.ProcessPid(), state.ExitStatus}
			}
			ch <- &state
			if err != nil || state.Exited {
				close(ch)
				return
			}

			isbreakpoint := false
			istracepoint := true
			for i := range state.Threads {
				if state.Threads[i].Breakpoint != nil {
					isbreakpoint = true
					istracepoint = istracepoint && state.Threads[i].Breakpoint.Tracepoint
				}
			}

			if !isbreakpoint || !istracepoint {
				close(ch)
				return
			}
		}
	}()
	return ch
}

// exitedToError returns an error if out.State says that the process exited.
func (c *RPCClient) exitedToError(out *CommandOut, err error) (*api.DebuggerState, error) {
	if err != nil {
		return nil, err
	}
	if out.State.Exited {
		return nil, &ProcessExitedError{c.ProcessPid(), out.State.ExitStatus}
	}
	return &out.State, nil
}

func (c *RPCClient) Next() (*api.DebuggerState, error) {
	var out CommandOut
	err := c.call("Command", api.DebuggerCommand{Name: api.Next, ReturnInfoLoadConfig: c.retValLoadCfg}, &out)
	return c.exitedToError(&out, err)
}

func (c *RPCClient) Step() (*api.DebuggerState, error) {
	var out CommandOut
	err := c.call("Command", api.DebuggerCommand{Name: api.Step, ReturnInfoLoadConfig: c.retValLoadCfg}, &out)
	return c.exitedToError(&out, err)
}

func (c *RPCClient) StepOut() (*api.DebuggerState, error) {
	var out CommandOut
	err := c.call("Command", &api.DebuggerCommand{Name: api.StepOut, ReturnInfoLoadConfig: c.retValLoadCfg}, &out)
	return c.exitedToError(&out, err)
}

func (c *RPCClient) StepInstruction() (*api.DebuggerState, error) {
	var out CommandOut
	err := c.call("Command", api.DebuggerCommand{Name: api.StepInstruction, ReturnInfoLoadConfig: c.retValLoadCfg}, &out)
	return c.exitedToError(&out, err)
}

func (c *RPCClient) SwitchThread(threadID int) (*api.DebuggerState, error) {
	var out CommandOut
	cmd := api.DebuggerCommand{
		Name:     api.SwitchThread,
		ThreadID: threadID,
	}
	err := c.call("Command", cmd, &out)
	return &out.State, err
}

func (c *RPCClient) SwitchGoroutine(goroutineID int) (*api.DebuggerState, error) {
	var out CommandOut
	cmd := api.DebuggerCommand{
		Name:        api.SwitchGoroutine,
		GoroutineID: goroutineID,
	}
	err := c.call("Command", cmd, &out)
	return &out.State, err
}

func (c *RPCClient) Halt() (*api.DebuggerState, error) {
	var out CommandOut
	err := c.call("Command", api.DebuggerCommand{Name: api.Halt}, &out)
	return &out.State, err
}

func (c *RPCClient) GetBreakpoint(id int) (*api.Breakpoint, error) {
	var out GetBreakpointOut
	err := c.call("GetBreakpoint", GetBreakpointIn{id, ""}, &out)
	return &out.Breakpoint, err
}

func (c *RPCClient) GetBreakpointByName(name string) (*api.Breakpoint, error) {
	var out GetBreakpointOut
	err := c.call("GetBreakpoint", GetBreakpointIn{0, name}, &out)
	return &out.Breakpoint, err
}

func (c *RPCClient) CreateBreakpoint(breakPoint *api.Breakpoint) (*api.Breakpoint, error) {
	var out CreateBreakpointOut
	err := c.call("CreateBreakpoint", CreateBreakpointIn{*breakPoint}, &out)
	return &out.Breakpoint, err
}

func (c *RPCClient) ListBreakpoints() ([]*api.Breakpoint, error) {
	var out ListBreakpointsOut
	err := c.call("ListBreakpoints", ListBreakpointsIn{}, &out)
	return out.Breakpoints, err
}

func (c *RPCClient) ClearBreakpoint(id int) (*api.Breakpoint, error) {
	var out ClearBreakpointOut
	err := c.call("ClearBreakpoint", ClearBreakpointIn{id, ""}, &out)
	return out.Breakpoint, err
}

func (c *RPCClient) ClearBreakpointByName(name string) (*api.Breakpoint, error) {
	var out ClearBreakpointOut
	err := c.call("ClearBreakpoint", ClearBreakpointIn{0, name}, &out)
	return out.Breakpoint, err
}

func (c *RPCClient) AmendBreakpoint(bp *api.Breakpoint) error {
	out := new(AmendBreakpointOut)
	err := c.call("AmendBreakpoint", AmendBreakpointIn{*bp}, out)
	return err
}

func (c *RPCClient) CancelNext() error {
	var out CancelNextOut
	return c.call("CancelNext", CancelNextIn{}, &out)
}

func (c *RPCClient) ListThreads() ([]*api.Thread, error) {
	var out ListThreadsOut
	err := c.call("ListThreads", ListThreadsIn{}, &out)
	return out.Threads, err
}

func (c *RPCClient) GetThread(id int) (*api.Thread, error) {
	var out GetThreadOut
	err := c.call("GetThread", GetThreadIn{id}, &out)
	return out.Thread, err
}

func (c *RPCClient) EvalVariable(scope api.EvalScope, expr string, cfg api.LoadConfig) (*api.Variable, error) {
	var out EvalOut
	err := c.call("Eval", EvalIn{scope, expr, &cfg}, &out)
	return out.Variable, err
}

func (c *RPCClient) SetVariable(scope api.EvalScope, symbol, value string) error {
	out := new(SetOut)
	return c.call("Set", SetIn{scope, symbol, value}, out)
}

func (c *RPCClient) ListSources(filter string) ([]string, error) {
	sources := new(ListSourcesOut)
	err := c.call("ListSources", ListSourcesIn{filter}, sources)
	return sources.Sources, err
}

func (c *RPCClient) ListFunctions(filter string) ([]string, error) {
	funcs := new(ListFunctionsOut)
	err := c.call("ListFunctions", ListFunctionsIn{filter}, funcs)
	return funcs.Funcs, err
}

func (c *RPCClient) ListTypes(filter string) ([]string, error) {
	types := new(ListTypesOut)
	err := c.call("ListTypes", ListTypesIn{filter}, types)
	return types.Types, err
}

func (c *RPCClient) ListPackageVariables(filter string, cfg api.LoadConfig) ([]api.Variable, error) {
	var out ListPackageVarsOut
	err := c.call("ListPackageVars", ListPackageVarsIn{filter, cfg}, &out)
	return out.Variables, err
}

func (c *RPCClient) ListLocalVariables(scope api.EvalScope, cfg api.LoadConfig) ([]api.Variable, error) {
	var out ListLocalVarsOut
	err := c.call("ListLocalVars", ListLocalVarsIn{scope, cfg}, &out)
	return out.Variables, err
}

func (c *RPCClient) ListRegisters(threadID int, includeFp bool) (api.Registers, error) {
	out := new(ListRegistersOut)
	err := c.call("ListRegisters", ListRegistersIn{ThreadID: threadID, IncludeFp: includeFp}, out)
	return out.Regs, err
}

func (c *RPCClient) ListFunctionArgs(scope api.EvalScope, cfg api.LoadConfig) ([]api.Variable, error) {
	var out ListFunctionArgsOut
	err := c.call("ListFunctionArgs", ListFunctionArgsIn{scope, cfg}, &out)
	return out.Args, err
}

func (c *RPCClient) ListGoroutines(start, count int) ([]*api.Goroutine, error) {
	var out ListGoroutinesOut
	err := c.call("ListGoroutines", ListGoroutinesIn{Start: start, Count: count}, &out)
	return out.Goroutines, err
}

func (c *RPCClient) Stacktrace(goroutineId, depth int, opts api.StacktraceOptions, cfg *api.LoadConfig) ([]api.Stackframe, error) {
	var out StacktraceOut
	readDefers := opts&api.StacktraceReadDefers != 0
	err := c.call("Stacktrace", StacktraceIn{goroutineId, depth, false, readDefers, opts, cfg}, &out)
	return out.Locations, err
}

func (c *RPCClient) AttachedToExistingProcess() bool {
	out := new(AttachedToExistingProcessOut)
	c.call("AttachedToExistingProcess", AttachedToExistingProcessIn{}, out)
	return out.Answer
}

func (c *RPCClient) FindLocation(scope api.EvalScope, loc string, findInstruction bool) ([]api.Location, error) {
	var out FindLocationOut
	err := c.call("FindLocation", FindLocationIn{scope, loc, !findInstruction}, &out)
	return out.Locations, err
}

// Disassemble code between startPC and endPC
func (c *RPCClient) DisassembleRange(scope api.EvalScope, startPC, endPC uint64, flavour api.AssemblyFlavour) (api.AsmInstructions, error) {
	var out DisassembleOut
	err := c.call("Disassemble", DisassembleIn{scope, startPC, endPC, flavour}, &out)
	return out.Disassemble, err
}

// Disassemble function containing pc
func (c *RPCClient) DisassemblePC(scope api.EvalScope, pc uint64, flavour api.AssemblyFlavour) (api.AsmInstructions, error) {
	var out DisassembleOut
	err := c.call("Disassemble", DisassembleIn{scope, pc, 0, flavour}, &out)
	return out.Disassemble, err
}

// Recorded returns true if the debugger target is a recording.
func (c *RPCClient) Recorded() bool {
	if c.recordedCache != nil {
		return *c.recordedCache
	}
	out := new(RecordedOut)
	c.call("Recorded", RecordedIn{}, out)
	c.recordedCache = &out.Recorded
	return out.Recorded
}

// TraceDirectory returns the path to the trace directory for a recording.
func (c *RPCClient) TraceDirectory() (string, error) {
	var out RecordedOut
	err := c.call("Recorded", RecordedIn{}, &out)
	return out.TraceDirectory, err
}

// Checkpoint sets a checkpoint at the current position.
func (c *RPCClient) Checkpoint(where string) (checkpointID int, err error) {
	var out CheckpointOut
	err = c.call("Checkpoint", CheckpointIn{where}, &out)
	return out.ID, err
}

// ListCheckpoints gets all checkpoints.
func (c *RPCClient) ListCheckpoints() ([]api.Checkpoint, error) {
	var out ListCheckpointsOut
	err := c.call("ListCheckpoints", ListCheckpointsIn{}, &out)
	return out.Checkpoints, err
}

// ClearCheckpoint removes a checkpoint
func (c *RPCClient) ClearCheckpoint(id int) error {
	var out ClearCheckpointOut
	err := c.call("ClearCheckpoint", ClearCheckpointIn{id}, &out)
	return err
}

func (c *RPCClient) Ancestors(goroutineID int, numAncestors int, depth int) ([]api.Ancestor, error) {
	var out AncestorsOut
	err := c.call("Ancestors", AncestorsIn{goroutineID, numAncestors, depth}, &out)
	return out.Ancestors, err
}

func (c *RPCClient) SetReturnValuesLoadConfig(cfg *api.LoadConfig) {
	c.retValLoadCfg = cfg
}

var errRunning = errors.New("running")

func (c *RPCClient) call(method string, args, reply interface{}) error {
	argsAsCmd := func() api.DebuggerCommand {
		cmd, ok := args.(api.DebuggerCommand)
		if !ok {
			pcmd := args.(*api.DebuggerCommand)
			cmd = *pcmd
		}
		return cmd
	}
	switch method {
	case "Command":
		cmd := argsAsCmd()
		switch cmd.Name {
		case api.SwitchThread, api.SwitchGoroutine, api.Halt:
			// those don't start the process
		default:
			c.mu.Lock()
			c.running = true
			c.mu.Unlock()
			defer func() {
				c.mu.Lock()
				c.running = false
				c.mu.Unlock()
			}()
		}
	case "Restart":
		c.mu.Lock()
		c.running = true
		c.mu.Unlock()
		defer func() {
			c.mu.Lock()
			c.running = false
			c.mu.Unlock()
		}()
	}

	return c.client.Call("RPCServer."+method, args, reply)
}

func (c *RPCClient) CallAPI(method string, args, reply interface{}) error {
	return c.call(method, args, reply)
}

func (c *RPCClient) IsMulticlient() bool {
	var out IsMulticlientOut
	c.call("IsMulticlient", IsMulticlientIn{}, &out)
	return out.IsMulticlient
}

func (c *RPCClient) Disconnect(cont bool) error {
	if cont {
		out := new(CommandOut)
		c.client.Go("RPCServer.Command", &api.DebuggerCommand{Name: api.Continue, ReturnInfoLoadConfig: c.retValLoadCfg}, &out, nil)
	}
	return c.client.Close()
}

func (c *RPCClient) GetStateNonBlocking() (*api.DebuggerState, error) {
	var out StateOut
	err := c.call("State", StateIn{NonBlocking: true}, &out)
	return out.State, err
}

func (c *RPCClient) WaitForRecordingDone() {
	c.mu.Lock()
	c.recording = true
	c.mu.Unlock()
	c.GetState()
	c.mu.Lock()
	c.recording = false
	c.mu.Unlock()
}

func (c *RPCClient) StopRecording() error {
	return c.call("StopRecording", StopRecordingIn{}, &StopRecordingOut{})
}

func (c *RPCClient) ReverseStep() (*api.DebuggerState, error) {
	var out CommandOut
	err := c.call("Command", api.DebuggerCommand{Name: api.ReverseNext, ReturnInfoLoadConfig: c.retValLoadCfg}, &out)
	return &out.State, err
}

func (c *RPCClient) ReverseNext() (*api.DebuggerState, error) {
	var out CommandOut
	err := c.call("Command", api.DebuggerCommand{Name: api.ReverseNext, ReturnInfoLoadConfig: c.retValLoadCfg}, &out)
	return &out.State, err
}

func (c *RPCClient) ReverseStepOut() (*api.DebuggerState, error) {
	var out CommandOut
	err := c.call("Command", api.DebuggerCommand{Name: api.ReverseStepOut, ReturnInfoLoadConfig: c.retValLoadCfg}, &out)
	return &out.State, err
}

func (c *RPCClient) ReverseStepInstruction() (*api.DebuggerState, error) {
	var out CommandOut
	err := c.call("Command", api.DebuggerCommand{Name: api.ReverseStepInstruction}, &out)
	return &out.State, err
}

func (c *RPCClient) DirectionCongruentContinue() <-chan *api.DebuggerState {
	return c.continueDir(api.DirectionCongruentContinue)
}
