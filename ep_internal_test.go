package ep

import (
	"context"
	"fmt"
	"github.com/panoplyio/ep/compare"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net"
	"sync"
	"testing"
)

var _ = Runners.
	Register("err", &errOnPort{}).
	Register("cancel", &waitForCancel{}).
	Register("dontCancel", &dontCancel{}).
	Register("dontCloseInp", &dontCloseInp{}).
	Register("drainInp", &drainInp{}).
	Register("fixedData", &fixedData{})

var _ = Types.Register("dummyString", str)
var str = &strType{}

type strType struct{}

func (s *strType) String() string     { return s.Name() }
func (*strType) Name() string         { return "string" }
func (*strType) Size() uint           { return 8 }
func (*strType) Data(n int) Data      { return make(strs, n) }
func (*strType) DataEmpty(n int) Data { return make(strs, 0, n) }

type strs []string

func (strs) Type() Type                                { return str }
func (vs strs) Len() int                               { return len(vs) }
func (vs strs) Less(int, int) bool                     { return false }
func (vs strs) Swap(int, int)                          {}
func (vs strs) LessOther(int, Data, int) bool          { return false }
func (vs strs) Slice(int, int) Data                    { return vs }
func (vs strs) Append(Data) Data                       { return vs }
func (vs strs) Duplicate(t int) Data                   { return vs }
func (vs strs) IsNull(int) bool                        { return false }
func (vs strs) MarkNull(int)                           {}
func (vs strs) Nulls() []bool                          { return make([]bool, vs.Len()) }
func (vs strs) Equal(Data) bool                        { return false }
func (vs strs) Compare(Data) ([]compare.Result, error) { return make([]compare.Result, vs.Len()), nil }
func (vs strs) Copy(Data, int, int)                    {}
func (vs strs) Strings() []string                      { return vs }

func startCluster(t *testing.T, ports ...string) []Distributer {
	res := make([]Distributer, len(ports))
	for i, port := range ports {
		ln, err := net.Listen("tcp", port)
		require.NoError(t, err)
		res[i] = NewDistributer(port, ln)
	}
	return res
}

func terminateCluster(t *testing.T, dists ...Distributer) {
	for _, d := range dists {
		// use assert and not require to make sure all dists will be closed
		assert.NoError(t, d.Close())
	}
}

type errOnPort struct {
	Port string
}

func (*errOnPort) Returns() []Type { return nil }
func (r *errOnPort) Run(ctx context.Context, inp, out chan Dataset) error {
	for data := range inp {
		out <- data
		if ctx.Value(thisNodeKey).(string) == r.Port {
			return fmt.Errorf("error from %s", r.Port)
		}
	}
	return nil
}

type waitForCancel struct{}

func (*waitForCancel) Returns() []Type { return nil }
func (r *waitForCancel) Run(ctx context.Context, inp, out chan Dataset) error {
	for {
		select {
		case data := <-inp:
			out <- data
		case <-ctx.Done(): // infinitely wait for cancel
			return nil
		}
	}
}

func closeWithoutCancel(r Runner, port string) Runner {
	return &dontCancel{r, port}
}

type dontCancel struct {
	Runner
	Port string
}

func (r *dontCancel) Returns() []Type { return r.Runner.Returns() }
func (r *dontCancel) Run(ctx context.Context, inp, out chan Dataset) error {
	if ctx.Value(thisNodeKey).(string) != r.Port && r.Port != "all" {
		return r.Runner.Run(ctx, inp, out)
	}

	var wg sync.WaitGroup
	internalInp := make(chan Dataset)
	wg.Add(1)
	go func() {
		for data := range inp {
			internalInp <- data
		}
		wg.Done()
	}()

	// to verify ctx will not cancel r.Runner, copy relevant content to new context
	internalCtx := context.Background()
	internalCtx = context.WithValue(internalCtx, distributerKey, ctx.Value(distributerKey))
	internalCtx = context.WithValue(internalCtx, allNodesKey, ctx.Value(allNodesKey))
	internalCtx = context.WithValue(internalCtx, masterNodeKey, ctx.Value(masterNodeKey))
	internalCtx = context.WithValue(internalCtx, thisNodeKey, ctx.Value(thisNodeKey))

	go func() {
		<-ctx.Done()
		wg.Wait()
		close(internalInp)
	}()

	return r.Runner.Run(internalCtx, internalInp, out)
}

func cancelWithoutClose(r Runner) Runner {
	return &dontCloseInp{r}
}

type dontCloseInp struct{ Runner }

func (r *dontCloseInp) Returns() []Type { return r.Runner.Returns() }
func (r *dontCloseInp) Run(ctx context.Context, inp, out chan Dataset) (err error) {
	var wg sync.WaitGroup
	internalInp := make(chan Dataset)
	internalOut := make(chan Dataset)

	go Run(ctx, r.Runner, internalInp, internalOut, nil, &err)

	wg.Add(1)
	go func() {
		for data := range inp {
			internalInp <- data
		}
		wg.Done()
	}()

	for data := range internalOut {
		out <- data
	}
	// only when internalOut is closed and internal runner exited without waiting
	// for inp, we can return and close inp
	wg.Wait()
	close(internalInp)
	return
}

type drainInp struct{}

func (*drainInp) Returns() []Type { return nil }
func (r *drainInp) Run(ctx context.Context, inp, out chan Dataset) error {
	for range inp {
	}
	return nil
}

type fixedData struct{}

func (*fixedData) Returns() []Type { return nil }
func (r *fixedData) Run(ctx context.Context, inp, out chan Dataset) error {
	out <- NewDataset(str.Data(2))
	out <- NewDataset(strs{"a", "b"})

	for data := range inp {
		out <- data
	}
	return nil
}
