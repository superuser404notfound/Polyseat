package seat

import (
	"strings"
	"testing"
)

// What a healthy seat writes, twice, because the journal holds every start it
// has ever had.
const probeOutput = `Found H.264 encoder: h264_nvenc
Found HEVC encoder: hevc_nvenc
Found AV1 encoder: av1_nvenc
Found H.264 encoder: h264_nvenc
Found HEVC encoder: hevc_nvenc
Found AV1 encoder: av1_nvenc`

// Reporting only H.264, which this did, reads as though H.264 were all a seat
// could do. Sunshine probes for three and offers whichever the client asks for.
func TestParseEncodersReportsEveryCodec(t *testing.T) {
	backend, codecs := parseEncoders(probeOutput)

	if backend != "nvenc" {
		t.Errorf("backend is %q, want nvenc", backend)
	}

	if strings.Join(codecs, ",") != "H.264,HEVC,AV1" {
		t.Errorf("codecs are %v, want H.264, HEVC and AV1 in that order", codecs)
	}
}

// The line that matters most. A seat that fell back to software starts,
// streams and looks entirely healthy until somebody plays on it.
func TestParseEncodersStillCatchesTheSoftwareFallback(t *testing.T) {
	backend, codecs := parseEncoders("Found H.264 encoder: libx264\nFound HEVC encoder: libx265")

	if backend != "libx264" {
		t.Errorf("backend is %q, want the software encoder named as it is", backend)
	}

	if !strings.HasPrefix(backend, "lib") {
		t.Errorf("%q would not be flagged as software by the interface", backend)
	}

	if len(codecs) != 2 {
		t.Errorf("codecs are %v, want both", codecs)
	}
}

// A seat's journal keeps every start, and only the last one describes what is
// running now. Reading an earlier one would report a card that has since been
// swapped, or a fallback long after it was fixed.
func TestParseEncodersTakesTheMostRecentProbe(t *testing.T) {
	backend, _ := parseEncoders(
		"Found H.264 encoder: libx264\nFound H.264 encoder: h264_nvenc")

	if backend != "nvenc" {
		t.Errorf("backend is %q, want the most recent probe", backend)
	}
}

func TestParseEncodersSurvivesNonsense(t *testing.T) {
	for label, out := range map[string]string{
		"empty":        "",
		"unrelated":    "Info: starting up\nWarning: something",
		"half a line":  "Found H.264 encoder:",
		"no separator": "Found H.264 encoder h264_nvenc",
	} {
		backend, codecs := parseEncoders(out)
		if backend != "" || len(codecs) != 0 {
			t.Errorf("%s: read %q %v out of nothing usable", label, backend, codecs)
		}
	}
}
