package protocol

import (
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
)

func (u *User) GetTypedAccount() (Account, error) {
	if u.GetAccount() == nil {
		return nil, errors.New("Account is missing")
	}

	rawAccount, err := u.Account.GetInstance()
	if err != nil {
		return nil, err
	}
	if asAccount, ok := rawAccount.(AsAccount); ok {
		return asAccount.AsAccount()
	}
	if account, ok := rawAccount.(Account); ok {
		return account, nil
	}
	return nil, errors.New("Unknown account type: ", u.Account.Type)
}

func (u *User) ToMemoryUser() (*MemoryUser, error) {
	account, err := u.GetTypedAccount()
	if err != nil {
		return nil, err
	}
	mu := &MemoryUser{
		Account: account,
		Email:   u.Email,
		Level:   u.Level,
	}
	if u.SendThrough != "" {
		ip := net.ParseIP(u.SendThrough)
		if ip == nil {
			return nil, errors.New("invalid send_through of user ", u.Email, ": ", u.SendThrough)
		}
		mu.SendThrough = net.IPAddress(ip)
	}
	return mu, nil
}

func ToProtoUser(mu *MemoryUser) *User {
	if mu == nil {
		return nil
	}
	u := &User{
		Account: serial.ToTypedMessage(mu.Account.ToProto()),
		Email:   mu.Email,
		Level:   mu.Level,
	}
	if mu.SendThrough != nil {
		u.SendThrough = mu.SendThrough.String()
	}
	return u
}

// MemoryUser is a parsed form of User, to reduce number of parsing of Account proto.
type MemoryUser struct {
	// Account is the parsed account of the protocol.
	Account Account
	Email   string
	Level   uint32
	// SendThrough is the source address of this user's outbound connections
	// when the outbound's sendThrough is "user". Nil if the user has none.
	SendThrough net.Address
}
