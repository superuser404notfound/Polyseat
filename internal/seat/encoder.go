package seat

import (
	"context"
	"strings"

	"github.com/superuser404notfound/Polyseat/internal/incusx"
)

// ReadEncoders reports which hardware path Sunshine settled on inside a seat
// and which codecs it can offer with it.
//
// The single most useful line in the whole interface is still whether the GPU
// path works: a seat that quietly fell back to software looks entirely healthy
// until somebody tries to play. But reporting only the H.264 encoder, which is
// what this did, reads as though H.264 were all a seat could do. Sunshine
// probes for three and offers whichever the client asks for, so the answer is
// a list.
//
// Expensive for what it is: it reads the seat's whole journal, which only grows,
// and greps it. Measured at 35 ms of the seat's own CPU against a 57 MB journal,
// which used to be spent every ten seconds forever. Sunshine probes once at
// startup and the answer cannot change while it runs, so encodersOnRecord keeps
// the daemon to once per Sunshine.
//
// It takes the uid rather than a Manager because the report wants the same
// answer without the daemon: a hardware report that says the seat streams but
// not what it encodes with leaves the one question every such report is opened
// to settle unanswered, and asking it costs a round trip to somebody whose
// machine nobody here can see.
func ReadEncoders(ctx context.Context, client *incusx.Client, name string, uid int64) (string, []string) {
	argv := append(playerPrefix(uid), "sh", "-c",
		"journalctl --user -u polyseat-sunshine.service --no-pager 2>/dev/null | "+
			"grep -oE 'Found (H\\.264|HEVC|AV1) encoder: [a-z0-9_]+'")

	out, _, err := client.Try(ctx, name, argv...)
	if err != nil {
		return "", nil
	}

	return parseEncoders(out)
}

// parseEncoders reads the lines Sunshine writes while probing.
//
// Separate from fetching them because a seat's journal holds every start it
// has ever had, and only the most recent probe describes what is running now:
// getting that backwards would report a card that has since been swapped, or a
// software fallback long after it was fixed.
func parseEncoders(out string) (string, []string) {
	seen := map[string]string{}
	order := []string{"H.264", "HEVC", "AV1"}

	for _, line := range strings.Split(out, "\n") {
		rest, found := strings.CutPrefix(strings.TrimSpace(line), "Found ")
		if !found {
			continue
		}

		codec, encoder, found := strings.Cut(rest, " encoder: ")
		if !found || encoder == "" {
			continue
		}

		// Later lines overwrite earlier ones, so what remains is the last run.
		seen[codec] = encoder
	}

	var codecs []string

	backend := ""

	for _, codec := range order {
		encoder, ok := seen[codec]
		if !ok {
			continue
		}

		codecs = append(codecs, codec)

		// Every codec of one run shares a backend, so the first says it.
		if backend == "" {
			if _, suffix, cut := strings.Cut(encoder, "_"); cut {
				backend = suffix
			} else {
				backend = encoder
			}
		}
	}

	return backend, codecs
}

// Software reports whether an encoder name is the CPU doing the work.
//
// Sunshine names its software encoders after the libraries they are, libx264
// and libx265, and every hardware one after the interface it goes through. The
// prefix is the whole of the rule, and it lives here so that the web interface,
// the provisioning log and the report cannot come to different conclusions
// about the same seat.
func Software(encoder string) bool {
	return strings.HasPrefix(encoder, "lib")
}
