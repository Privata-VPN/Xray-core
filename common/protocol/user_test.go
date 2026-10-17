package protocol_test

import (
	"testing"

	. "github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/uuid"
	"github.com/xtls/xray-core/proxy/vless"
)

func TestUserSendThrough(t *testing.T) {
	id := uuid.New()
	account := serial.ToTypedMessage(&vless.Account{Id: id.String()})

	mu, err := (&User{Email: "alice", Account: account, SendThrough: "10.0.98.5"}).ToMemoryUser()
	if err != nil {
		t.Fatal(err)
	}
	if got := mu.SendThrough.String(); got != "10.0.98.5" {
		t.Errorf("SendThrough %v, want 10.0.98.5", got)
	}
	if got := ToProtoUser(mu).SendThrough; got != "10.0.98.5" {
		t.Errorf("proto SendThrough %q, want 10.0.98.5", got)
	}

	mu, err = (&User{Email: "bob", Account: account}).ToMemoryUser()
	if err != nil {
		t.Fatal(err)
	}
	if mu.SendThrough != nil || ToProtoUser(mu).SendThrough != "" {
		t.Errorf("user without send_through got %v", mu.SendThrough)
	}

	if _, err := (&User{Email: "carol", Account: account, SendThrough: "10.0.98"}).ToMemoryUser(); err == nil {
		t.Error("accepted an invalid send_through")
	}
}
