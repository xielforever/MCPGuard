package metadata

import (
	"bytes"
	"testing"
)

func TestProtectedPayloadBinding(t *testing.T) {
	v, e := NewVault(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if e != nil {
		t.Fatal(e)
	}
	data := []byte(`{"itemId":"item_demo_1"}`)
	sealed := v.Seal(data, "org/action/PLAN/local-v1")
	if bytes.Contains(sealed, data) {
		t.Fatal("plaintext persisted")
	}
	opened, e := v.Open(sealed, "org/action/PLAN/local-v1")
	if e != nil || !bytes.Equal(opened, data) {
		t.Fatal(e)
	}
	for _, binding := range []string{"other/action/PLAN/local-v1", "org/action/RESULT/local-v1"} {
		if _, e = v.Open(sealed, binding); e == nil {
			t.Fatal("binding was not authenticated")
		}
	}
	sealed[len(sealed)-1] ^= 1
	if _, e = v.Open(sealed, "org/action/PLAN/local-v1"); e == nil {
		t.Fatal("tampering accepted")
	}
	if len(v.Hash(data)) != 43 {
		t.Fatal("wrong plan hash format")
	}
	if _, e = NewVault(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{1}, 32)); e == nil {
		t.Fatal("same key used for two purposes")
	}
}
