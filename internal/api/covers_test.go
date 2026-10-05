package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestCoverWireContract(t *testing.T) {
	c, _ := apiFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case "GET":
			fmt.Fprint(w, `{"data":{"items":[{"id":"cover-1","uploadId":"upload-1","kind":"custom","state":"ready"}],"recordingVersion":4}}`)
		case "POST":
			if r.Header.Get("Content-Type") != "image/png" || r.Header.Get("Idempotency-Key") != "upload-key" {
				t.Error("wrong upload headers")
			}
			w.WriteHeader(202)
			fmt.Fprint(w, `{"data":{"uploadId":"upload-1","state":"pending"}}`)
		case "PUT":
			if r.Header.Get("If-Match") != `"4"` || r.Header.Get("Idempotency-Key") != "select-key" {
				t.Error("wrong selection headers")
			}
			fmt.Fprintf(w, `{"data":{"recording":{"id":%q,"version":5,"status":"draft"},"coverId":"cover-1","outcome":"selected","receipt":{"coverId":"cover-1","recordingVersion":5}}}`, recordingID)
		}
	}, false, "cms:recordings:read", "cms:recordings:write")
	if _, err := c.ListCovers(context.Background(), recordingID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UploadCover(context.Background(), recordingID, []byte("fixture"), "image/png", "upload-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SelectCover(context.Background(), recordingID, "upload-1", 4, "select-key"); err != nil {
		t.Fatal(err)
	}
}
