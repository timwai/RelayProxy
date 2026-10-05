package protocol

import (
	"bytes"
	"encoding/binary"
)

const (
	PublicDirectTicketVersion         = 1
	PublicDirectTicketCapabilityProxy = "proxy"
)

type PublicDirectTicketClaims struct {
	Version               int      `json:"version"`
	Issuer                string   `json:"issuer"`
	ClientDeviceID        string   `json:"clientDeviceId"`
	ExitDeviceID          string   `json:"exitDeviceId"`
	IssuedAt              int64    `json:"issuedAt"`
	ExpiresAt             int64    `json:"expiresAt"`
	PolicyRevision        int64    `json:"policyRevision"`
	AuthorizationRevision int64    `json:"authorizationRevision"`
	Nonce                 []byte   `json:"nonce"`
	AllowedCapabilities   []string `json:"allowedCapabilities"`
}

type PublicDirectSignedTicket struct {
	Claims    PublicDirectTicketClaims `json:"claims"`
	Signature []byte                   `json:"signature"`
}

// PublicDirectTicketPayload is the exact byte sequence signed by the Server.
// It is deliberately independent of JSON encoding so a ticket cannot acquire
// multiple equivalent serialized forms with different signature semantics.
func PublicDirectTicketPayload(claims PublicDirectTicketClaims) []byte {
	var out bytes.Buffer
	out.WriteString("RelayProxy public direct ticket\x00")
	_ = binary.Write(&out, binary.BigEndian, uint32(claims.Version))
	writeDirectTicketField(&out, []byte(claims.Issuer))
	writeDirectTicketField(&out, []byte(claims.ClientDeviceID))
	writeDirectTicketField(&out, []byte(claims.ExitDeviceID))
	_ = binary.Write(&out, binary.BigEndian, claims.IssuedAt)
	_ = binary.Write(&out, binary.BigEndian, claims.ExpiresAt)
	_ = binary.Write(&out, binary.BigEndian, claims.PolicyRevision)
	_ = binary.Write(&out, binary.BigEndian, claims.AuthorizationRevision)
	writeDirectTicketField(&out, claims.Nonce)
	_ = binary.Write(&out, binary.BigEndian, uint32(len(claims.AllowedCapabilities)))
	for _, capability := range claims.AllowedCapabilities {
		writeDirectTicketField(&out, []byte(capability))
	}
	return out.Bytes()
}

func writeDirectTicketField(out *bytes.Buffer, value []byte) {
	_ = binary.Write(out, binary.BigEndian, uint32(len(value)))
	out.Write(value)
}
