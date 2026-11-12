package modrt

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/agent-wow/agent-wow/pkg/modules/discovery"
	"github.com/agent-wow/agent-wow/pkg/modules/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/anypb"
)

const callTimeout = 30 * time.Second

type callKind uint8

const (
	publicCall callKind = iota
	internalCall
	packetCall
	logoutCall
)

// invocationAllowed is called with mu held, both during lookup and immediately
// before forwarding. Decoding a large request must not bypass a logout gate.
func (m *Manager) invocationAllowed(inst *instance, public bool) bool {
	return !m.closing && m.err == nil && !m.prepared && !(public && m.preparing) && inst != nil && inst.healthy && !inst.prepared
}

func (m *Manager) lookup(name, alias string, public bool) (*instance, moddisc.Method, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.registry.Lookup(name)
	if !ok {
		return nil, moddisc.Method{}, status.Error(codes.NotFound, "module not found")
	}
	md, ok := d.RPC(alias)
	if !ok {
		return nil, moddisc.Method{}, status.Error(codes.NotFound, "method not found")
	}
	inst := m.instances[name]
	if !m.invocationAllowed(inst, public) {
		return nil, moddisc.Method{}, status.Error(codes.FailedPrecondition, "module invocation unavailable in current session state")
	}
	return inst, md, nil
}

func (m *Manager) InvokeJSON(ctx context.Context, full string, params json.RawMessage) (json.RawMessage, error) {
	if full == "session.describe" {
		return m.describe(params)
	}
	name, alias, _ := strings.Cut(full, ".")
	fault := func(code int, err error) (json.RawMessage, error) {
		return nil, &CallError{Code: code, Module: name, Method: alias, Err: err}
	}
	inst, md, err := m.lookup(name, alias, true)
	if err != nil {
		code := -32000
		if status.Code(err) == codes.NotFound {
			code = -32601
		}
		return fault(code, err)
	}
	params = bytes.TrimSpace(params)
	if len(params) == 0 || bytes.Equal(params, []byte("null")) {
		params = []byte("{}")
	}
	if len(params) == 0 || params[0] != '{' {
		return fault(-32602, status.Error(codes.InvalidArgument, "params must be an object"))
	}
	req := dynamicpb.NewMessage(md.Descriptor().Input())
	if err := (protojson.UnmarshalOptions{Resolver: inst.definition.Types()}).Unmarshal(params, req); err != nil {
		return fault(-32602, status.Error(codes.InvalidArgument, err.Error()))
	}
	result := dynamicpb.NewMessage(md.Descriptor().Output())
	if err := m.invoke(ctx, publicCall, "jsonrpc", name, inst, md, req, result); err != nil {
		return fault(-32000, err)
	}
	body, err := (protojson.MarshalOptions{Resolver: inst.definition.Types()}).Marshal(result)
	if err != nil {
		return fault(-32000, status.Error(codes.Internal, err.Error()))
	}
	return json.RawMessage(body), nil
}

func (m *Manager) invokeModule(ctx context.Context, callerName string, req *modv1.InvokeModuleRequest) (*modv1.InvokeModuleResponse, error) {
	caller, ok := m.registry.Lookup(callerName)
	if !ok || !caller.Requires(req.GetModule()) {
		return nil, status.Error(codes.PermissionDenied, "target is not a directly declared dependency")
	}
	inst, md, err := m.lookup(req.GetModule(), req.GetMethod(), false)
	if err != nil {
		return nil, err
	}
	input := dynamicpb.NewMessage(md.Descriptor().Input())
	if req.Request == nil || !req.Request.MessageIs(input) {
		return nil, status.Error(codes.InvalidArgument, "request type does not match target method")
	}
	if err := anypb.UnmarshalTo(req.Request, input, proto.UnmarshalOptions{Resolver: inst.definition.Types()}); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	result := dynamicpb.NewMessage(md.Descriptor().Output())
	if err := m.invoke(ctx, internalCall, callerName, req.Module, inst, md, input, result); err != nil {
		return nil, err
	}
	packed, err := anypb.New(result)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &modv1.InvokeModuleResponse{Result: packed}, nil
}

func (m *Manager) invoke(ctx context.Context, kind callKind, caller, name string, inst *instance, md moddisc.Method, req, result proto.Message) (err error) {
	started := time.Now()
	defer func() {
		m.logger.Debug("module call", "caller", caller, "module", name, "method", md.Path(), "duration", time.Since(started), "status", status.Code(err).String())
	}()
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	m.mu.Lock()
	deadline := m.prepareDeadline
	allowed := kind != publicCall && kind != internalCall || m.invocationAllowed(inst, kind == publicCall)
	m.mu.Unlock()
	if !allowed {
		return status.Error(codes.FailedPrecondition, "module invocation unavailable in current session state")
	}
	if (kind == publicCall || kind == internalCall) && !deadline.IsZero() {
		var end context.CancelFunc
		ctx, end = context.WithDeadline(ctx, deadline)
		defer end()
	}
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	err = inst.conn.Invoke(ctx, md.Path(), req, result)
	if status.Code(err) == codes.Unavailable {
		m.fail(name, err)
	}
	return err
}
