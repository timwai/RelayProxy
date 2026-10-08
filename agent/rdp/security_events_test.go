package rdp

import (
	"encoding/binary"
	"testing"
	"unicode/utf16"
)

const sampleSecurityXML = `<?xml version="1.0" encoding="utf-8"?>
<Events xmlns="http://schemas.microsoft.com/win/2004/08/events/event">
<Event><System><EventID>4625</EventID><EventRecordID>144</EventRecordID><TimeCreated SystemTime="2026-10-08T10:00:01.1234567Z"/></System>
<EventData><Data Name="LogonType">3</Data><Data Name="IpAddress">::ffff:127.0.0.1</Data><Data Name="IpPort">50123</Data>
<Data Name="TargetUserName">用户一</Data><Data Name="Status">0xC000006D</Data><Data Name="SubStatus">0xC000006A</Data></EventData></Event>
<Event><System><EventID>4625</EventID><EventRecordID>145</EventRecordID><TimeCreated SystemTime="2026-10-08T10:00:02Z"/></System>
<EventData><Data Name="LogonType">10</Data><Data Name="IpAddress">127.0.0.1</Data><Data Name="IpPort">0</Data></EventData></Event>
<Event><System><EventID>4625</EventID><EventRecordID>146</EventRecordID><TimeCreated SystemTime="2026-10-08T10:00:02Z"/></System>
<EventData><Data Name="LogonType">3</Data><Data Name="IpAddress">203.0.113.8</Data><Data Name="IpPort">50000</Data></EventData></Event>
</Events>`

func TestDecodeWindowsRDP4625(t *testing.T) {
	events, err := DecodeWindowsRDP4625([]byte(sampleSecurityXML))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].RecordID != 144 || events[0].SourcePort != 50123 ||
		events[0].Username != "用户一" || events[1].SourcePort != 0 {
		t.Fatalf("incorrect parsed Windows audit records: %+v", events)
	}
}

func TestDecodeWindowsRDP4625UTF16(t *testing.T) {
	raw := []rune(sampleSecurityXML)
	encoded := utf16.Encode(raw)
	b := make([]byte, 2+len(encoded)*2)
	b[0], b[1] = 0xff, 0xfe
	for i, value := range encoded {
		binary.LittleEndian.PutUint16(b[2+i*2:], value)
	}
	events, err := DecodeWindowsRDP4625(b)
	if err != nil || len(events) != 2 || events[0].Username != "用户一" {
		t.Fatalf("UTF-16 Windows audit not decoded: %+v %v", events, err)
	}
}

func TestDecodeWindowsRDP4625MalformedXML(t *testing.T) {
	if _, err := DecodeWindowsRDP4625([]byte("<Events><Event>")); err == nil {
		t.Fatal("malformed XML accepted")
	}
}
