//go:build ignore

// Command peverify inspects a Windows PE image and reports the fields that
// matter for a desktop application: the subsystem, the embedded icon group, and
// whether the application manifest carries per-monitor DPI awareness.
//
// It exists because two of the desktop-rewrite requirements are invisible in
// source and only observable in the linked binary:
//
//   - "no console window" is a property of the PE subsystem, set by the
//     -H=windowsgui linker flag. A GUI-named binary linked as a console app
//     still flashes a black window on double-click.
//   - "transparent icon, not a black square" depends on the icon group that
//     makes it into the resource directory, and on its ID being one that the
//     runtime actually probes.
//
// Usage:
//
//	go run scripts/peverify.go dist/windows-amd64/relay-agent-gui.exe
//	go run scripts/peverify.go dist/windows-amd64/*.exe
//
// Exit code is 0 even when a file looks wrong: this is a reporting tool, not a
// gate. Read the reported subsystem and RT_GROUP_ICON ids.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"unicode/utf16"
)

var machines = map[uint16]string{
	0x014c: "i386", 0x8664: "amd64", 0xaa64: "arm64", 0x01c4: "armv7",
}

var subsystems = map[uint16]string{
	1: "native", 2: "WINDOWS_GUI", 3: "WINDOWS_CONSOLE", 5: "os2_cui",
	7: "posix_cui", 9: "windows_ce_gui", 10: "efi_application",
}

var resTypes = map[uint32]string{
	1: "CURSOR", 2: "BITMAP", 3: "ICON", 4: "MENU", 5: "DIALOG", 6: "STRING",
	12: "GROUP_CURSOR", 14: "GROUP_ICON", 16: "VERSION", 24: "MANIFEST",
}

// manifestMarkers are the manifest features the desktop build depends on.
var manifestMarkers = []string{
	"dpiAware>", "dpiAwareness>", "Common-Controls",
	"requestedExecutionLevel", "longPathAware>",
}

type section struct {
	name           string
	va, vsize, raw uint32
	rawSize        uint32
}

type image struct {
	data    []byte
	rva2off func(uint32) (uint32, bool)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: go run scripts/peverify.go <file.exe> [file.exe ...]")
		os.Exit(2)
	}
	for _, path := range os.Args[1:] {
		if err := dump(path); err != nil {
			fmt.Printf("  ERROR  %v\n", err)
		}
		fmt.Println()
	}
}

func dump(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fmt.Printf("=== %s  (%d bytes)\n", path, len(data))
	if len(data) < 0x40 || data[0] != 'M' || data[1] != 'Z' {
		return fmt.Errorf("not a PE image")
	}
	peOff := int(binary.LittleEndian.Uint32(data[0x3C:]))
	if peOff+24 > len(data) || !bytes.Equal(data[peOff:peOff+4], []byte("PE\x00\x00")) {
		return fmt.Errorf("bad PE signature at 0x%X", peOff)
	}
	coff := peOff + 4
	machine := binary.LittleEndian.Uint16(data[coff:])
	nSec := int(binary.LittleEndian.Uint16(data[coff+2:]))
	sizeOpt := int(binary.LittleEndian.Uint16(data[coff+16:]))
	opt := coff + 20
	magic := binary.LittleEndian.Uint16(data[opt:])
	subsystem := binary.LittleEndian.Uint16(data[opt+68:])

	fmt.Printf("  machine    : 0x%04X %s\n", machine, machines[machine])
	fmt.Printf("  magic      : 0x%04X %s\n", magic, map[uint16]string{0x10b: "PE32", 0x20b: "PE32+"}[magic])
	fmt.Printf("  subsystem  : %d %s\n", subsystem, subsystems[subsystem])

	secTable := opt + sizeOpt
	sections := make([]section, 0, nSec)
	for i := 0; i < nSec; i++ {
		b := data[secTable+i*40:]
		sections = append(sections, section{
			name:    string(bytes.TrimRight(b[:8], "\x00")),
			vsize:   binary.LittleEndian.Uint32(b[8:]),
			va:      binary.LittleEndian.Uint32(b[12:]),
			rawSize: binary.LittleEndian.Uint32(b[16:]),
			raw:     binary.LittleEndian.Uint32(b[20:]),
		})
	}
	rva2off := func(rva uint32) (uint32, bool) {
		for _, s := range sections {
			span := s.vsize
			if s.rawSize > span {
				span = s.rawSize
			}
			if rva >= s.va && rva < s.va+span {
				return s.raw + (rva - s.va), true
			}
		}
		return 0, false
	}

	var dirOff, nDirOff int
	if magic == 0x20b {
		nDirOff, dirOff = opt+108, opt+112
	} else {
		nDirOff, dirOff = opt+92, opt+96
	}
	nDirs := binary.LittleEndian.Uint32(data[nDirOff:])
	if nDirs < 3 {
		return fmt.Errorf("no resource directory")
	}
	resRVA := binary.LittleEndian.Uint32(data[dirOff+2*8:])
	if resRVA == 0 {
		fmt.Println("  resources  : NONE")
		return nil
	}
	resOff, ok := rva2off(resRVA)
	if !ok {
		return fmt.Errorf("resource RVA 0x%X not mapped", resRVA)
	}

	fmt.Println("  resources  :")
	img := &image{data: data, rva2off: rva2off}
	walkRes(img, resOff, 0, resTypes)

	if txt := findManifest(img, resOff); txt != "" {
		fmt.Printf("  manifest   : %d bytes\n", len(txt))
		for _, key := range manifestMarkers {
			if i := bytes.Index([]byte(txt), []byte(key)); i >= 0 {
				end := i + 60
				if end > len(txt) {
					end = len(txt)
				}
				fmt.Printf("    %-24s %s\n", key, oneLine(txt[i:end]))
			} else {
				fmt.Printf("    %-24s MISSING\n", key)
			}
		}
	} else {
		fmt.Println("  manifest   : NOT EMBEDDED")
	}
	return nil
}

// walkRes prints one level of the resource tree: resource types at the root,
// then the id or name of each entry beneath them.
func walkRes(img *image, dirOff uint32, depth int, types map[uint32]string) {
	d := img.data
	nNamed := int(binary.LittleEndian.Uint16(d[dirOff+12:]))
	nID := int(binary.LittleEndian.Uint16(d[dirOff+14:]))
	for i := 0; i < nNamed+nID; i++ {
		e := dirOff + 16 + uint32(i*8)
		nameField := binary.LittleEndian.Uint32(d[e:])
		offField := binary.LittleEndian.Uint32(d[e+4:])
		label := ""
		switch {
		case nameField&0x80000000 != 0:
			label = readResString(img, dirOff+nameField&0x7FFFFFFF)
		case depth == 0:
			if t, ok := types[nameField]; ok {
				label = "RT_" + t
			} else {
				label = fmt.Sprintf("type %d", nameField)
			}
		default:
			label = fmt.Sprintf("id %d", nameField)
		}
		if depth == 0 && nameField&0x80000000 == 0 {
			fmt.Printf("    %s\n", label)
		} else {
			fmt.Printf("      %s\n", label)
		}
		if offField&0x80000000 != 0 && depth < 1 {
			walkRes(img, dirOff+offField&0x7FFFFFFF, depth+1, types)
		}
	}
}

func oneLine(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		out = append(out, r)
	}
	return string(out)
}

func readResString(img *image, off uint32) string {
	d := img.data
	if int(off)+2 > len(d) {
		return "?"
	}
	n := int(binary.LittleEndian.Uint16(d[off:]))
	u := make([]uint16, 0, n)
	for i := 0; i < n; i++ {
		p := int(off) + 2 + i*2
		if p+2 > len(d) {
			break
		}
		u = append(u, binary.LittleEndian.Uint16(d[p:]))
	}
	return string(utf16.Decode(u))
}

// findManifest locates RT_MANIFEST and returns its bytes as text.
func findManifest(img *image, resOff uint32) string {
	d := img.data
	nTop := int(binary.LittleEndian.Uint16(d[resOff+12:])) + int(binary.LittleEndian.Uint16(d[resOff+14:]))
	for i := 0; i < nTop; i++ {
		e := resOff + 16 + uint32(i*8)
		typeID := binary.LittleEndian.Uint32(d[e:])
		offField := binary.LittleEndian.Uint32(d[e+4:])
		if typeID != 24 || offField&0x80000000 == 0 {
			continue
		}
		// Walk the type -> name -> language levels, then read the data entry.
		sub := resOff + offField&0x7FFFFFFF
		n2 := int(binary.LittleEndian.Uint16(d[sub+12:])) + int(binary.LittleEndian.Uint16(d[sub+14:]))
		for j := 0; j < n2; j++ {
			off2 := binary.LittleEndian.Uint32(d[sub+16+uint32(j*8)+4:])
			if off2&0x80000000 == 0 {
				continue
			}
			sub2 := resOff + off2&0x7FFFFFFF
			n3 := int(binary.LittleEndian.Uint16(d[sub2+12:])) + int(binary.LittleEndian.Uint16(d[sub2+14:]))
			for k := 0; k < n3; k++ {
				off3 := binary.LittleEndian.Uint32(d[sub2+16+uint32(k*8)+4:])
				if off3&0x80000000 != 0 {
					continue // deeper level; manifests sit at the leaf
				}
				dataEntry := resOff + off3
				fileOff := binary.LittleEndian.Uint32(d[dataEntry:])
				size := binary.LittleEndian.Uint32(d[dataEntry+4:])
				fo, ok := img.rva2off(fileOff)
				if !ok || int(fo)+int(size) > len(d) {
					continue
				}
				return string(d[fo : fo+size])
			}
		}
	}
	return ""
}
