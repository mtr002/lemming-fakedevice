// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fault

import (
	"context"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	gpb "github.com/openconfig/gnmi/proto/gnmi"
	plqpb "github.com/openconfig/gnoi/packet_link_qualification"
	spb "github.com/openconfig/gnoi/system"

	faultpb "github.com/openconfig/lemming/proto/fault"
)

// NewCient creates a new fault client.
func NewClient(conn grpc.ClientConnInterface) *Client {
	return &Client{
		fc: faultpb.NewFaultInjectClient(conn),
	}
}

// Client is a client for the lemming fault service.
type Client struct {
	fc faultpb.FaultInjectClient
}

// GNMISubscribe starts intercepting gnmi.Subscribe calls.
func (c *Client) GNMISubscribe(t testing.TB) *StreamClient[*gpb.SubscribeRequest, *gpb.SubscribeResponse] {
	t.Helper()
	sc, err := newStreamClient[*gpb.SubscribeRequest, *gpb.SubscribeResponse]("/gnmi.gNMI/Subscribe", c.fc)
	if err != nil {
		t.Fatal(err)
	}

	go sc.process()

	return sc
}

// GNMISet starts intercepting gnmi.Set calls.
func (c *Client) GNMISet(t testing.TB) *UnaryClient[*gpb.SetRequest, *gpb.SetResponse] {
	t.Helper()
	uc, err := newUnaryClient[*gpb.SetRequest, *gpb.SetResponse]("/gnmi.gNMI/Set", c.fc)
	if err != nil {
		t.Fatal(err)
	}

	go uc.process()

	return uc
}

func newStreamClient[ReqT, RespT proto.Message](rpc string, fc faultpb.FaultInjectClient) (*StreamClient[ReqT, RespT], error) {
	ctx, cancel := context.WithCancel(context.Background())

	s, err := fc.Intercept(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	err = s.Send(&faultpb.InterceptRequest{Msg: &faultpb.InterceptRequest_IntSub{
		IntSub: &faultpb.InterceptSubRequest{
			Method: rpc,
		},
	}})
	if err != nil {
		cancel()
		return nil, err
	}

	sc := &StreamClient[ReqT, RespT]{
		ic:       s,
		cancelFn: cancel,
	}
	return sc, nil
}

// StreamClient is fault client for streaming RPC.
type StreamClient[ReqT, RespT proto.Message] struct {
	mu           sync.Mutex
	ic           faultpb.FaultInject_InterceptClient
	cancelFn     func()
	reqCallBack  func(ReqT) (ReqT, error)
	respCallBack func(RespT) (RespT, error)
}

// Stop stops intercepting RPCs.
func (sc *StreamClient[ReqT, RespT]) Stop() {
	sc.cancelFn()
}

// SetReqCallback modifies the incoming requests before the server sees them.
func (sc *StreamClient[ReqT, RespT]) SetReqCallback(cb func(ReqT) (ReqT, error)) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.reqCallBack = cb
}

// SetRespCallback modiifies outgoing requests after the are sent by the server.
func (sc *StreamClient[ReqT, RespT]) SetRespCallback(cb func(RespT) (RespT, error)) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.respCallBack = cb
}

func (sc *StreamClient[ReqT, RespT]) process() {
	for {
		msg, err := sc.ic.Recv()
		if err != nil {
			return
		}
		switch msg.GetOriginalMsg().MsgType {
		case faultpb.MessageType_MESSAGE_TYPE_REQUEST:
			req, err := msg.GetOriginalMsg().GetMsg().UnmarshalNew()
			if err != nil {
				return
			}
			// By default, return the original message and status
			fMsg := msg.GetOriginalMsg().GetMsg()
			fSt := msg.GetOriginalMsg().GetStatus()

			sc.mu.Lock()
			cb := sc.reqCallBack
			sc.mu.Unlock()

			// If a callback exists, run it and uses it's returns
			if cb != nil {
				faultReq, faultErr := cb(req.(ReqT))
				fMsg, _ = anypb.New(faultReq)
				st, _ := status.FromError(faultErr)
				fSt = st.Proto()
			}
			sc.ic.Send(&faultpb.InterceptRequest{
				Msg: &faultpb.InterceptRequest_FaultMsg{
					FaultMsg: &faultpb.FaultMessage{
						MsgId:  msg.GetOriginalMsg().GetMsgId(),
						Msg:    fMsg,
						Status: fSt,
					},
				},
			})
		case faultpb.MessageType_MESSAGE_TYPE_RESPONSE:
			resp, err := msg.GetOriginalMsg().GetMsg().UnmarshalNew()
			if err != nil {
				return
			}
			// By default, return the original message and status
			fMsg := msg.GetOriginalMsg().GetMsg()
			fSt := msg.GetOriginalMsg().GetStatus()

			sc.mu.Lock()
			cb := sc.respCallBack
			sc.mu.Unlock()

			// If a callback exists, run it and uses it's returns
			if cb != nil {
				faultReq, faultErr := cb(resp.(RespT))
				fMsg, _ = anypb.New(faultReq)
				st, _ := status.FromError(faultErr)
				fSt = st.Proto()
			}
			sc.ic.Send(&faultpb.InterceptRequest{
				Msg: &faultpb.InterceptRequest_FaultMsg{
					FaultMsg: &faultpb.FaultMessage{
						MsgId:  msg.GetOriginalMsg().GetMsgId(),
						Msg:    fMsg,
						Status: fSt,
					},
				},
			})
		case faultpb.MessageType_MESSAGE_TYPE_STREAM_END:
			sc.ic.Send(&faultpb.InterceptRequest{
				Msg: &faultpb.InterceptRequest_FaultMsg{
					FaultMsg: &faultpb.FaultMessage{
						MsgId:  msg.GetOriginalMsg().GetMsgId(),
						Msg:    msg.GetOriginalMsg().GetMsg(),
						Status: msg.GetOriginalMsg().GetStatus(),
					},
				},
			})
		}
	}
}

func newUnaryClient[ReqT, RespT proto.Message](rpc string, fc faultpb.FaultInjectClient) (*UnaryClient[ReqT, RespT], error) {
	ctx, cancel := context.WithCancel(context.Background())

	s, err := fc.Intercept(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	err = s.Send(&faultpb.InterceptRequest{Msg: &faultpb.InterceptRequest_IntSub{
		IntSub: &faultpb.InterceptSubRequest{
			Method: rpc,
		},
	}})
	if err != nil {
		cancel()
		return nil, err
	}

	uc := &UnaryClient[ReqT, RespT]{
		ic:       s,
		cancelFn: cancel,
	}
	return uc, nil
}

// UnaryClient is a fault client for unary RPC.
type UnaryClient[ReqT, RespT proto.Message] struct {
	mu       sync.Mutex
	ic       faultpb.FaultInject_InterceptClient
	cancelFn func()
	reqMod   func(ReqT) ReqT
	respMod  func(RespT, error) (RespT, error)
	bypass   func(ReqT) (RespT, error)
}

func (uc *UnaryClient[ReqT, RespT]) Stop() {
	uc.cancelFn()
}

// SetReqMod modifies incoming requests. The RPC server processes the modified request.
// Use this function to inject invalid requests to server.
func (uc *UnaryClient[ReqT, RespT]) SetReqMod(cb func(ReqT) ReqT) {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	uc.reqMod = cb
}

// SetReqMod sets a func modifies outgoing response. The  RPC server still processed the RPC normall, only the returned value is changed.
// Use this func to "lie" about the state of the server.
func (uc *UnaryClient[ReqT, RespT]) SetRespMod(cb func(RespT, error) (RespT, error)) {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	uc.respMod = cb
}

// SetBypass sets a func that bypasses the server processing and returns an error.
// The func MUST return a non-nil error, else the server isn't bypassed.
func (uc *UnaryClient[ReqT, RespT]) SetBypass(cb func(ReqT) (RespT, error)) {
	uc.mu.Lock()
	defer uc.mu.Unlock()
	uc.bypass = cb
}

func (sc *UnaryClient[ReqT, RespT]) process() {
	for {
		msg, err := sc.ic.Recv()
		if err != nil {
			return
		}
		switch msg.GetOriginalMsg().MsgType {
		case faultpb.MessageType_MESSAGE_TYPE_REQUEST:
			req, err := msg.GetOriginalMsg().GetMsg().UnmarshalNew()
			if err != nil {
				return
			}
			// By default, return the original message and status
			fMsg := msg.GetOriginalMsg().GetMsg()
			fSt := msg.GetOriginalMsg().GetStatus()

			sc.mu.Lock()
			rm := sc.reqMod
			bypass := sc.bypass
			sc.mu.Unlock()

			// If a callback exists, run it and uses it's returns
			if bypass != nil {
				faultResp, faultErr := bypass(req.(ReqT))
				fMsg, _ = anypb.New(faultResp)
				st, _ := status.FromError(faultErr)
				fSt = st.Proto()
			} else if rm != nil {
				faultReq := rm(req.(ReqT))
				fMsg, _ = anypb.New(faultReq)
			}
			sc.ic.Send(&faultpb.InterceptRequest{
				Msg: &faultpb.InterceptRequest_FaultMsg{
					FaultMsg: &faultpb.FaultMessage{
						MsgId:  msg.GetOriginalMsg().GetMsgId(),
						Msg:    fMsg,
						Status: fSt,
					},
				},
			})
		case faultpb.MessageType_MESSAGE_TYPE_RESPONSE:
			resp, err := msg.GetOriginalMsg().GetMsg().UnmarshalNew()
			if err != nil {
				return
			}
			// By default, return the original message and status
			fMsg := msg.GetOriginalMsg().GetMsg()
			fSt := msg.GetOriginalMsg().GetStatus()

			sc.mu.Lock()
			cb := sc.respMod
			sc.mu.Unlock()

			// If a callback exists, run it and uses it's returns
			if cb != nil {
				faultReq, faultErr := cb(resp.(RespT), status.FromProto(fSt).Err())
				fMsg, _ = anypb.New(faultReq)
				st, _ := status.FromError(faultErr)
				fSt = st.Proto()
			}
			sc.ic.Send(&faultpb.InterceptRequest{
				Msg: &faultpb.InterceptRequest_FaultMsg{
					FaultMsg: &faultpb.FaultMessage{
						MsgId:  msg.GetOriginalMsg().GetMsgId(),
						Msg:    fMsg,
						Status: fSt,
					},
				},
			})
		}
	}
}

// ---------------------
// gNOI System Methods
// ---------------------

// GNOIReboot starts intercepting gnoi.system.System/Reboot calls.
func (c *Client) GNOIReboot(t testing.TB) *UnaryClient[*spb.RebootRequest, *spb.RebootResponse] {
	t.Helper()
	uc, err := newUnaryClient[*spb.RebootRequest, *spb.RebootResponse]("/gnoi.system.System/Reboot", c.fc)
	if err != nil {
		t.Fatal(err)
	}

	go uc.process()

	return uc
}

// GNOICancelReboot starts intercepting gnoi.system.System/CancelReboot calls.
func (c *Client) GNOICancelReboot(t testing.TB) *UnaryClient[*spb.CancelRebootRequest, *spb.CancelRebootResponse] {
	t.Helper()
	uc, err := newUnaryClient[*spb.CancelRebootRequest, *spb.CancelRebootResponse]("/gnoi.system.System/CancelReboot", c.fc)
	if err != nil {
		t.Fatal(err)
	}

	go uc.process()

	return uc
}

// GNOISwitchControlProcessor starts intercepting gnoi.system.System/SwitchControlProcessor calls.
func (c *Client) GNOISwitchControlProcessor(t testing.TB) *UnaryClient[*spb.SwitchControlProcessorRequest, *spb.SwitchControlProcessorResponse] {
	t.Helper()
	uc, err := newUnaryClient[*spb.SwitchControlProcessorRequest, *spb.SwitchControlProcessorResponse]("/gnoi.system.System/SwitchControlProcessor", c.fc)
	if err != nil {
		t.Fatal(err)
	}

	go uc.process()

	return uc
}

// GNOIKillProcess starts intercepting gnoi.system.System/KillProcess calls.
func (c *Client) GNOIKillProcess(t testing.TB) *UnaryClient[*spb.KillProcessRequest, *spb.KillProcessResponse] {
	t.Helper()
	uc, err := newUnaryClient[*spb.KillProcessRequest, *spb.KillProcessResponse]("/gnoi.system.System/KillProcess", c.fc)
	if err != nil {
		t.Fatal(err)
	}

	go uc.process()

	return uc
}

// GNOITime starts intercepting gnoi.system.System/Time calls.
func (c *Client) GNOITime(t testing.TB) *UnaryClient[*spb.TimeRequest, *spb.TimeResponse] {
	t.Helper()
	uc, err := newUnaryClient[*spb.TimeRequest, *spb.TimeResponse]("/gnoi.system.System/Time", c.fc)
	if err != nil {
		t.Fatal(err)
	}

	go uc.process()

	return uc
}

// GNOIPing starts intercepting gnoi.system.System/Ping calls.
func (c *Client) GNOIPing(t testing.TB) *StreamClient[*spb.PingRequest, *spb.PingResponse] {
	t.Helper()
	sc, err := newStreamClient[*spb.PingRequest, *spb.PingResponse]("/gnoi.system.System/Ping", c.fc)
	if err != nil {
		t.Fatal(err)
	}

	go sc.process()

	return sc
}

// --------------------------------
// gNOI Link Qualification Methods
// --------------------------------

// GNOILinkQualificationCapabilities starts intercepting gnoi.packet_link_qualification.LinkQualification/Capabilities calls.
func (c *Client) GNOILinkQualificationCapabilities(t testing.TB) *UnaryClient[*plqpb.CapabilitiesRequest, *plqpb.CapabilitiesResponse] {
	t.Helper()
	uc, err := newUnaryClient[*plqpb.CapabilitiesRequest, *plqpb.CapabilitiesResponse]("/gnoi.packet_link_qualification.LinkQualification/Capabilities", c.fc)
	if err != nil {
		t.Fatal(err)
	}

	go uc.process()

	return uc
}

// GNOILinkQualificationCreate starts intercepting gnoi.packet_link_qualification.LinkQualification/Create calls.
func (c *Client) GNOILinkQualificationCreate(t testing.TB) *UnaryClient[*plqpb.CreateRequest, *plqpb.CreateResponse] {
	t.Helper()
	uc, err := newUnaryClient[*plqpb.CreateRequest, *plqpb.CreateResponse]("/gnoi.packet_link_qualification.LinkQualification/Create", c.fc)
	if err != nil {
		t.Fatal(err)
	}

	go uc.process()

	return uc
}

// GNOILinkQualificationGet starts intercepting gnoi.packet_link_qualification.LinkQualification/Get calls.
func (c *Client) GNOILinkQualificationGet(t testing.TB) *UnaryClient[*plqpb.GetRequest, *plqpb.GetResponse] {
	t.Helper()
	uc, err := newUnaryClient[*plqpb.GetRequest, *plqpb.GetResponse]("/gnoi.packet_link_qualification.LinkQualification/Get", c.fc)
	if err != nil {
		t.Fatal(err)
	}

	go uc.process()

	return uc
}

// GNOILinkQualificationDelete starts intercepting gnoi.packet_link_qualification.LinkQualification/Delete calls.
func (c *Client) GNOILinkQualificationDelete(t testing.TB) *UnaryClient[*plqpb.DeleteRequest, *plqpb.DeleteResponse] {
	t.Helper()
	uc, err := newUnaryClient[*plqpb.DeleteRequest, *plqpb.DeleteResponse]("/gnoi.packet_link_qualification.LinkQualification/Delete", c.fc)
	if err != nil {
		t.Fatal(err)
	}

	go uc.process()

	return uc
}

// GNOILinkQualificationList starts intercepting gnoi.packet_link_qualification.LinkQualification/List calls.
func (c *Client) GNOILinkQualificationList(t testing.TB) *UnaryClient[*plqpb.ListRequest, *plqpb.ListResponse] {
	t.Helper()
	uc, err := newUnaryClient[*plqpb.ListRequest, *plqpb.ListResponse]("/gnoi.packet_link_qualification.LinkQualification/List", c.fc)
	if err != nil {
		t.Fatal(err)
	}

	go uc.process()

	return uc
}
