package wireids

import (
	"encoding/json"
	"testing"
)

func TestTypedExactRoundTripAndCancellation(t *testing.T) {
	for _, id := range []string{`1`, `-1`, `9007199254740993`, `9223372036854775808`, `1234567890123456789012345678901234567890`, `"1"`, `"mgid1.MQ"`, `"员工"`} {
		raw := []byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"tools/list"}`)
		in, e := Incoming(raw)
		if e != nil {
			t.Fatal(e)
		}
		var m map[string]json.RawMessage
		_ = json.Unmarshal(in, &m)
		if len(m["id"]) == 0 || m["id"][0] != '"' {
			t.Fatal("ID was not encoded for SDK")
		}
		out, e := Outgoing([]byte(`{"jsonrpc":"2.0","id":` + string(m["id"]) + `,"result":{}}`))
		if e != nil {
			t.Fatal(e)
		}
		var restored map[string]json.RawMessage
		_ = json.Unmarshal(out, &restored)
		want, _ := canonicalID([]byte(id))
		if string(restored["id"]) != string(want) {
			t.Fatalf("ID changed: %s", id)
		}
		cancel, e := Incoming([]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":` + id + `}}`))
		if e != nil {
			t.Fatal(e)
		}
		var c struct {
			Params struct {
				RequestID json.RawMessage `json:"requestId"`
			} `json:"params"`
		}
		_ = json.Unmarshal(cancel, &c)
		if string(c.Params.RequestID) != string(m["id"]) {
			t.Fatal("cancel target differs from request ID")
		}
	}
	if _, e := Incoming([]byte(`{"jsonrpc":"2.0","id":1.5,"method":"tools/list"}`)); e == nil {
		t.Fatal("fractional ID accepted")
	}
	if _, e := Incoming([]byte(`{"jsonrpc":"2.0","id":1e99999999,"method":"tools/list"}`)); e == nil {
		t.Fatal("unbounded number expansion accepted")
	}
	if _, e := Incoming([]byte(`{"jsonrpc":"2.0","id":1,"id":2,"method":"tools/list"}`)); e == nil {
		t.Fatal("duplicate envelope ID accepted")
	}
	a, _ := encode([]byte(`"foo"`))
	b, _ := encode([]byte(`"\u0066oo"`))
	if string(a) != string(b) {
		t.Fatal("equivalent string IDs differ")
	}
}
