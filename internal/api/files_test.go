package api

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"
)

// drop builds the body a browser really sends: one part per file, each carrying
// the path under the folder that was dropped as its file name. A form is
// allowed to carry ordinary fields as well, and this puts one in front so that
// the reader has to walk past it.
func drop(t *testing.T, files [][2]string) *multipart.Reader {
	t.Helper()

	body := &bytes.Buffer{}
	form := multipart.NewWriter(body)

	if err := form.WriteField("seat", "living-room"); err != nil {
		t.Fatal(err)
	}

	for _, file := range files {
		part, err := form.CreateFormFile("file", file[0])
		if err != nil {
			t.Fatal(err)
		}

		if _, err := io.WriteString(part, file[1]); err != nil {
			t.Fatal(err)
		}
	}

	if err := form.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest("POST", "/api/seats/living-room/files", body)
	request.Header.Set("Content-Type", form.FormDataContentType())

	reader, err := request.MultipartReader()
	if err != nil {
		t.Fatal(err)
	}

	return reader
}

// The path is what the seat package validates and writes to, so what arrives
// here has to be the path the browser sent and not the last component of it.
// A folder upload is one part per file and the folder lives in the name.
func TestAnUploadKeepsTheNameTheBrowserSent(t *testing.T) {
	parts := &uploadParts{parts: drop(t, [][2]string{
		{"Eden.AppImage", "one"},
		{"load/0100F2C0115B6000/Textures/romfs/a.bfres", "two"},
	})}

	for _, want := range []string{"Eden.AppImage", "load/0100F2C0115B6000/Textures/romfs/a.bfres"} {
		up, err := parts.Next()
		if err != nil {
			t.Fatalf("%s: %v", want, err)
		}

		if up.Path != want {
			t.Errorf("the part arrived as %q, and the folder it was in is the whole point", up.Path)
		}
	}

	if _, err := parts.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("the end of the body reported %v, and Receive ends its loop on io.EOF alone", err)
	}
}

// The bodies have to be readable in order and match their own part. Reading
// them out of step is the failure this shape of interface invites: one reader
// over one connection, handed out one part at a time.
func TestAnUploadReadsEachBodyInTurn(t *testing.T) {
	parts := &uploadParts{parts: drop(t, [][2]string{
		{"first.bin", "the first file"},
		{"second.bin", "the second file"},
	})}

	for _, want := range []string{"the first file", "the second file"} {
		up, err := parts.Next()
		if err != nil {
			t.Fatal(err)
		}

		got, err := io.ReadAll(up.Body)
		if err != nil {
			t.Fatal(err)
		}

		if string(got) != want {
			t.Errorf("read %q, want %q", got, want)
		}
	}
}

// A file the seat refuses is never read, and the next one still has to arrive
// whole. This is what lets Receive skip a badly named file and carry on with
// the drop: if an unread part could run into the one behind it, the file after
// a rejected one would arrive corrupted instead of missing, which is the worse
// of the two failures and the quieter one.
func TestAnUploadSurvivesAFileNobodyRead(t *testing.T) {
	parts := &uploadParts{parts: drop(t, [][2]string{
		{"../refused", strings.Repeat("x", 40000)},
		{"kept.bin", "what should arrive"},
	})}

	if _, err := parts.Next(); err != nil {
		t.Fatal(err)
	}

	up, err := parts.Next()
	if err != nil {
		t.Fatal(err)
	}

	if up.Path != "kept.bin" {
		t.Fatalf("the part after the unread one is %q", up.Path)
	}

	got, err := io.ReadAll(up.Body)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != "what should arrive" {
		t.Errorf("the file after an unread one arrived as %q", got)
	}
}

// A form field is not a file. It has no file name, and passing it on would have
// the seat refuse an upload nobody made with a sentence about names.
func TestAnUploadWalksPastWhatIsNotAFile(t *testing.T) {
	parts := &uploadParts{parts: drop(t, [][2]string{{"only.bin", "content"}})}

	up, err := parts.Next()
	if err != nil {
		t.Fatal(err)
	}

	if up.Path != "only.bin" {
		t.Errorf("the first thing handed over was %q, and the field before it is not a file", up.Path)
	}
}
