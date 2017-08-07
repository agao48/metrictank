package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/google/go-querystring/query"
	opentracing "github.com/opentracing/opentracing-go"
	tags "github.com/opentracing/opentracing-go/ext"
	"github.com/raintank/worldping-api/pkg/log"
)

//go:generate stringer -type=NodeState
type NodeState int

const (
	NodeNotReady NodeState = iota
	NodeReady
	NodeUnreachable
)

func NodeStateFromString(s string) NodeState {
	switch s {
	case "NodeNotReady":
		return NodeNotReady
	case "NodeReady":
		return NodeReady
	case "NodeUnreachable":
		return NodeUnreachable
	}
	//default
	return NodeNotReady
}

type Error struct {
	code int
	err  error
}

func NewError(code int, err error) *Error {
	return &Error{
		code: code,
		err:  err,
	}
}

// implement errors.Error interface
func (r *Error) Error() string {
	return r.err.Error()
}

// implement response.Response
func (r *Error) Code() int {
	return r.code
}

type Node struct {
	Name          string    `json:"name"`
	Version       string    `json:"version"`
	Primary       bool      `json:"primary"`
	PrimaryChange time.Time `json:"primaryChange"`
	State         NodeState `json:"state"`
	Priority      int       `json:"priority"`
	Started       time.Time `json:"started"`
	StateChange   time.Time `json:"stateChange"`
	Partitions    []int32   `json:"partitions"`
	ApiPort       int       `json:"apiPort"`
	ApiScheme     string    `json:"apiScheme"`
	Updated       time.Time `json:"updated"`
	RemoteAddr    string    `json:"remoteAddr"`
	local         bool
}

func (n Node) RemoteURL() string {
	return fmt.Sprintf("%s://%s:%d", n.ApiScheme, n.RemoteAddr, n.ApiPort)
}

func (n Node) IsReady() bool {
	return n.State == NodeReady && n.Priority <= maxPrio
}

func (n Node) IsLocal() bool {
	return n.local
}

func (n Node) Post(ctx context.Context, name, path string, body Traceable) ([]byte, error) {

	span := opentracing.SpanFromContext(ctx)
	span = Tracer.StartSpan(name, opentracing.ChildOf(span.Context()))
	tags.SpanKindRPCClient.Set(span)
	tags.PeerService.Set(span, "metrictank")
	tags.PeerAddress.Set(span, n.RemoteAddr)
	tags.PeerHostname.Set(span, n.Name)
	body.Trace(span)
	ctx = opentracing.ContextWithSpan(ctx, span)
	defer span.Finish()

	b, err := json.Marshal(body)
	if err != nil {
		tags.Error.Set(span, true)
		return nil, NewError(http.StatusInternalServerError, err)
	}
	var reader *bytes.Reader
	reader = bytes.NewReader(b)
	addr := n.RemoteURL() + path
	req, err := http.NewRequest("POST", addr, reader)
	if err != nil {
		tags.Error.Set(span, true)
		return nil, NewError(http.StatusInternalServerError, err)
	}
	carrier := opentracing.HTTPHeadersCarrier(req.Header)
	err = Tracer.Inject(span.Context(), opentracing.HTTPHeaders, carrier)
	if err != nil {
		log.Error(3, "CLU failed to inject span into headers: %s", err)
	}
	req.Header.Add("Content-Type", "application/json")
	rsp, err := client.Do(req)
	if err != nil {
		tags.Error.Set(span, true)
		log.Error(3, "CLU Node: %s unreachable. %s", n.Name, err.Error())
		return nil, NewError(http.StatusServiceUnavailable, fmt.Errorf("cluster node unavailable"))
	}
	out, err := handleResp(rsp)
	if err != nil {
		tags.Error.Set(span, true)
	}
	return out, err
}

func handleResp(rsp *http.Response) ([]byte, error) {
	defer rsp.Body.Close()
	if rsp.StatusCode != 200 {
		return nil, NewError(rsp.StatusCode, fmt.Errorf(rsp.Status))
	}
	return ioutil.ReadAll(rsp.Body)
}

// Convert an interface{} to a urlencoded querystring
func toQueryString(q interface{}) (string, error) {
	v, err := query.Values(q)
	if err != nil {
		return "", err
	}
	return v.Encode(), nil
}
