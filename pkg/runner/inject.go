package runner

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/pkg/nemesis"
	"github.com/kemalhafizh/strife/pkg/transport"
)

// Injector applies fault transitions to the nodes of a cluster. A test run
// and `strife bench inject` share it, so the benchmark measures exactly the
// code path that runs during a test.
type Injector struct {
	Transport transport.Transport
	Nodes     []transport.Node
	Env       nemesis.Env
	// CommandTimeout bounds each command or agent call (default 30s).
	CommandTimeout time.Duration
}

func (in *Injector) timeout() time.Duration {
	if in.CommandTimeout > 0 {
		return in.CommandTimeout
	}
	return 30 * time.Second
}

// Method names how faults are delivered: the transport name, or
// "agent:direct" when the transport applies structured faults itself.
func (in *Injector) Method() string {
	if _, ok := in.Transport.(transport.FaultApplier); ok {
		return "agent:direct"
	}
	return in.Transport.Name()
}

// Exec runs shell commands concurrently, one goroutine per command, and
// returns one message per failed command.
func (in *Injector) Exec(ctx context.Context, cmds []nemesis.Command) []string {
	var (
		mu   sync.Mutex
		errs []string
		wg   sync.WaitGroup
	)
	for _, c := range cmds {
		wg.Add(1)
		go func(c nemesis.Command) {
			defer wg.Done()
			n := in.Nodes[c.Node]
			cctx, cancel := context.WithTimeout(ctx, in.timeout())
			defer cancel()
			var err error
			if c.Host {
				_, err = in.Transport.HostExec(cctx, n, c.Script)
			} else {
				_, err = in.Transport.Exec(cctx, n, c.Script)
			}
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Sprintf("%s: %v", n.Name, err))
				mu.Unlock()
			}
		}(c)
	}
	wg.Wait()
	return errs
}

// Apply starts (start=true) or ends fault f on every node it involves and
// returns once every node has confirmed, with one message per failure.
func (in *Injector) Apply(ctx context.Context, f nemesis.Fault, start bool) []string {
	fa, ok := in.Transport.(transport.FaultApplier)
	if !ok {
		if start {
			return in.Exec(ctx, in.Env.Inject(f))
		}
		return in.Exec(ctx, in.Env.Heal(f))
	}
	targets := f.Nodes
	if f.Kind == nemesis.Partition {
		// Both sides drop the other side's traffic.
		targets = make([]int, len(in.Nodes))
		for i := range in.Nodes {
			targets[i] = i
		}
	}
	var (
		mu   sync.Mutex
		errs []string
		wg   sync.WaitGroup
	)
	for _, i := range targets {
		wg.Add(1)
		go func(n transport.Node) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, in.timeout())
			defer cancel()
			var err error
			if start {
				_, err = fa.InjectFault(cctx, n, f, in.Env)
			} else {
				_, err = fa.HealFault(cctx, n, f, in.Env)
			}
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Sprintf("%s: %v", n.Name, err))
				mu.Unlock()
			}
		}(in.Nodes[i])
	}
	wg.Wait()
	return errs
}

// Teardown heals everything Strife may have touched for the given kinds. It
// is safe to call more than once.
func (in *Injector) Teardown(ctx context.Context, kinds []nemesis.Kind) []string {
	if fa, ok := in.Transport.(transport.FaultApplier); ok {
		var errs []string
		for _, n := range in.Nodes {
			if err := fa.TeardownNode(ctx, n); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", n.Name, err))
			}
		}
		if len(errs) == 0 {
			return nil // the agents healed everything they applied
		}
		return append(errs, in.Exec(ctx, in.Env.Teardown(kinds))...)
	}
	return in.Exec(ctx, in.Env.Teardown(kinds))
}

// Setup prepares the nodes for the given fault kinds. Transports that apply
// faults themselves (strife-agent) need no shell setup: it would only
// require tools the agent does not use (iptables in every pod, say). Those
// with slow lookups do them now, so the first fault lands on time.
func (in *Injector) Setup(ctx context.Context, kinds []nemesis.Kind) []string {
	if _, ok := in.Transport.(transport.FaultApplier); ok {
		if p, ok := in.Transport.(transport.Preparer); ok {
			if err := p.Prepare(ctx, in.Nodes); err != nil {
				return []string{err.Error()}
			}
		}
		return nil
	}
	return in.Exec(ctx, in.Env.Setup(kinds))
}
