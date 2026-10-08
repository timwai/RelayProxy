package rdp

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"relayproxy/internal/protocol"
)

type windowsSecurityEvent struct {
	System struct {
		EventID uint32 `xml:"EventID"`
		RecordID uint64 `xml:"EventRecordID"`
		TimeCreated struct { SystemTime string `xml:"SystemTime,attr"` } `xml:"TimeCreated"`
	} `xml:"System"`
	EventData struct {
		Data []struct {
			Name string `xml:"Name,attr"`
			Value string `xml:",chardata"`
		} `xml:"Data"`
	} `xml:"EventData"`
}

// DecodeWindowsRDP4625 parses the structured Security event log, not human
// readable/localized message text. Zero/missing source port is retained for
// uncorrelated auditing but is not eligible for IP banning.
func DecodeWindowsRDP4625(raw []byte) ([]protocol.RDPHostAuthFailure,error) {
	data := raw
	if len(raw) >= 2 && ((raw[0] == 0xff && raw[1] == 0xfe) || (raw[0] == 0xfe && raw[1] == 0xff)) {
		little := raw[0] == 0xff
		u16 := make([]uint16,0,len(raw)/2)
		for i:=2;i+1<len(raw);i+=2 {
			var value uint16
			if little { value=uint16(raw[i])|uint16(raw[i+1])<<8 } else { value=uint16(raw[i+1])|uint16(raw[i])<<8 }
			u16=append(u16,value)
		}
		data=[]byte(string(utf16.Decode(u16)))
		// wevtutil's output may retain its UTF-16 XML encoding declaration.
		data=bytes.ReplaceAll(data,[]byte("utf-16"),[]byte("utf-8"))
		data=bytes.ReplaceAll(data,[]byte("UTF-16"),[]byte("UTF-8"))
	}
	reader := xml.NewDecoder(bytes.NewReader(data))
	events := make([]protocol.RDPHostAuthFailure,0)
	for {
		token,err:=reader.Token()
		if err == io.EOF { return events,nil }
		if err != nil { return nil,err }
		start,ok:=token.(xml.StartElement)
		if !ok || start.Name.Local != "Event" { continue }
		var item windowsSecurityEvent
		if err := reader.DecodeElement(&item,&start); err != nil { return nil,err }
		if item.System.EventID != 4625 || item.System.RecordID == 0 { continue }
		fields:=make(map[string]string,len(item.EventData.Data))
		for _,d:=range item.EventData.Data { fields[d.Name]=strings.TrimSpace(d.Value) }
		logonType,err:=strconv.Atoi(fields["LogonType"])
		if err!=nil || (logonType!=3 && logonType!=10) { continue }
		ip,err:=netip.ParseAddr(fields["IpAddress"])
		if err!=nil || !ip.Unmap().IsLoopback() { continue }
		sourcePort:=0
		if rawPort:=fields["IpPort"]; rawPort!="" && rawPort!="-" {
			port,err:=strconv.Atoi(rawPort)
			if err!=nil || port<0 || port>65535 { continue }
			sourcePort=port
		}
		observed,err:=time.Parse(time.RFC3339Nano,item.System.TimeCreated.SystemTime)
		if err!=nil { continue }
		events=append(events,protocol.RDPHostAuthFailure{
			RecordID:item.System.RecordID,SourceAddress:ip.Unmap().String(),SourcePort:sourcePort,
			ObservedAt:observed.UTC(),Username:fields["TargetUserName"],Status:fields["Status"],
			SubStatus:fields["SubStatus"],LogonType:logonType,
		})
	}
}
