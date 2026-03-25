package rpc

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestWriteAPIErrorJSONShape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeAPIError(rec, 400, "invalid_id", "bad id")

	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.NewDecoder(rec.Result().Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "bad id" || body.Code != "invalid_id" {
		t.Fatalf("got %+v", body)
	}
	if rec.Code != 400 {
		t.Fatalf("status %d", rec.Code)
	}
}
