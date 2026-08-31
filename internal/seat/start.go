package seat

import (
	"context"
	"fmt"

	"github.com/superuser404notfound/Polyseat/internal/hostcg"
	"github.com/superuser404notfound/Polyseat/internal/incusx"
)

// startContainer starts a seat's container, and if that fails for the one host
// reason this daemon can do something about, does that and tries once more.
//
// Looked at before, acted on after, and the split is the whole of what this
// function knows that the obvious version does not.
//
// Acting only after a failure is what keeps this from being a gate. Asking the
// host about its cgroup layout first and refusing when it looks wrong would be
// a claim that the start cannot work, and a claim like that is only as good as
// its author's reading of a version of LXC that has changed before and will
// again. Get it wrong and a machine that would have run seats is told it cannot,
// with no way for the person in front of it to disagree.
//
// Looking before is because of what a failing start does to the evidence. LXC
// makes cgroups of its own in every hierarchy on the host, moves the processes
// of the attempt through them, and tears them down when the attempt dies. Asked
// afterwards, the host says the hierarchy has processes in it — the failure's
// own, on their way out — and this daemon reads that as somebody relying on it
// and leaves it alone. It is a failure vouching for its own irreparability, and
// it happened on the machine this was written for: two processes reported, none
// a second later, and a cgroup by then gone that no exclusion list would have
// been written for in advance. Before the start nothing of the kind is running,
// so the answer is about the host rather than about the attempt.
//
// The retry is one, not a loop. If clearing the hierarchy was not what the start
// needed, a second failure is the real answer and should be reported as it is.
func startContainer(ctx context.Context, client *incusx.Client, name string, log Logger) error {
	// While the host is quiet. On a machine with nothing wrong this reads one
	// file, finds no legacy mount and walks nothing.
	before := hostcg.Mounts()

	err := client.Start(ctx, name)
	if err == nil {
		return nil
	}

	if len(before) == 0 {
		return err
	}

	if !hostcg.RecoverFor(before, log) {
		return fmt.Errorf("%w\n\n%s", err, hostcg.Describe(before))
	}

	return client.Start(ctx, name)
}
