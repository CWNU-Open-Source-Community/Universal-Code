package releasegate

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const approvedBrandMasterSHA256 = "a97e71e5f373d5d48ce24e39360cd390be2beabfe731037ac114ec38f12407d6"

// Generated from the approved globe-and-terminal master, including both COFF
// resources with the shared PerMonitorV2/asInvoker manifest.
const approvedBrandDerivativeSetSHA256 = "98e5cc53e87c82c0b6557608b6a40cac40046bf91223c42d8d2af1803806068c"

func TestApprovedBrandMasterAndPlatformAssetsStaySynchronized(t *testing.T) {
	t.Parallel()
	root := filepath.Clean(filepath.Join("..", ".."))
	masterPath := filepath.Join(root, "assets", "branding", "traverse-board-mark.png")
	master := readTestFile(t, masterPath)
	sum := sha256.Sum256(master)
	if got := hex.EncodeToString(sum[:]); got != approvedBrandMasterSHA256 {
		t.Fatalf("brand master SHA-256 = %s, want %s", got, approvedBrandMasterSHA256)
	}
	assertPNGDimensions(t, masterPath, master, 1254, false)

	for _, asset := range []struct {
		path string
		size int
	}{
		{filepath.Join("web", "src", "assets", "traverse-board-mark.png"), 512},
		{filepath.Join("web", "public", "traverse-board-favicon-32.png"), 32},
		{filepath.Join("web", "public", "apple-touch-icon.png"), 180},
		{filepath.Join("packaging", "windows", "Assets", "StoreLogo.png"), 50},
		{filepath.Join("packaging", "windows", "Assets", "Square44x44Logo.png"), 44},
		{filepath.Join("packaging", "windows", "Assets", "Square150x150Logo.png"), 150},
		{filepath.Join("packaging", "windows", "StoreListingIcon.png"), 300},
	} {
		path := filepath.Join(root, asset.path)
		content := readTestFile(t, path)
		assertPNGDimensions(t, path, content, asset.size, true)
		if strings.Contains(filepath.ToSlash(asset.path), "packaging/windows/") && len(content) >= 204800 {
			t.Fatalf("Windows package/listing image %s is %d bytes; WACK limit is below 204800", asset.path, len(content))
		}
	}

	for _, path := range []string{
		filepath.Join(root, "assets", "branding", "README.md"),
		filepath.Join(root, "docs", "branding", "README.md"),
	} {
		content := string(readTestFile(t, path))
		if !strings.Contains(content, approvedBrandMasterSHA256) {
			t.Fatalf("%s does not identify the approved brand master", path)
		}
	}

	generatedPaths := []string{
		filepath.Join("web", "src", "assets", "traverse-board-mark.png"),
		filepath.Join("web", "public", "traverse-board-favicon-32.png"),
		filepath.Join("web", "public", "apple-touch-icon.png"),
		filepath.Join("packaging", "windows", "StoreListingIcon.png"),
		filepath.Join("packaging", "windows", "TraverseBoard.ico"),
		filepath.Join("packaging", "macos", "TraverseBoard.icns"),
		filepath.Join("cmd", "cyberagent-desktop", "traverse_board_windows_amd64.syso"),
		filepath.Join("cmd", "cyberagent-desktop", "traverse_board_windows_arm64.syso"),
	}
	windowsAssets, err := os.ReadDir(filepath.Join(root, "packaging", "windows", "Assets"))
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range windowsAssets {
		if current.IsDir() {
			t.Fatalf("Windows visual-asset set contains a directory: %s", current.Name())
		}
		generatedPaths = append(generatedPaths,
			filepath.Join("packaging", "windows", "Assets", current.Name()))
	}
	sort.Strings(generatedPaths)
	derivativeSet := sha256.New()
	for _, relative := range generatedPaths {
		_, _ = io.WriteString(derivativeSet, filepath.ToSlash(relative))
		_, _ = derivativeSet.Write([]byte{0})
		_, _ = derivativeSet.Write(readTestFile(t, filepath.Join(root, relative)))
		_, _ = derivativeSet.Write([]byte{0})
	}
	if got := hex.EncodeToString(derivativeSet.Sum(nil)); got != approvedBrandDerivativeSetSHA256 {
		t.Fatalf("generated brand-asset set SHA-256 = %s, want %s", got, approvedBrandDerivativeSetSHA256)
	}

	workflow := string(readTestFile(t, filepath.Join(root, ".github", "workflows", "release-desktop.yml")))
	for _, requiredPath := range []string{
		"scripts/generate-brand-assets.ps1",
		"scripts/windows-visual-assets.ps1",
		"assets/branding/**",
		"packaging/macos/**",
	} {
		if !strings.Contains(workflow, requiredPath) {
			t.Fatalf("Desktop release workflow does not watch %q", requiredPath)
		}
	}
}

func TestWindowsIconContainerAndExecutableResourcesStaySynchronized(t *testing.T) {
	t.Parallel()
	root := filepath.Clean(filepath.Join("..", ".."))
	ico := readTestFile(t, filepath.Join(root, "packaging", "windows", "TraverseBoard.ico"))
	if len(ico) < 6 || binary.LittleEndian.Uint16(ico[0:2]) != 0 ||
		binary.LittleEndian.Uint16(ico[2:4]) != 1 {
		t.Fatal("TraverseBoard.ico has an invalid ICONDIR header")
	}
	wantSizes := []int{16, 20, 24, 30, 32, 36, 40, 48, 60, 64, 72, 80, 96, 128, 256}
	count := int(binary.LittleEndian.Uint16(ico[4:6]))
	if count != len(wantSizes) || len(ico) < 6+16*count {
		t.Fatalf("TraverseBoard.ico entry count = %d, want %d", count, len(wantSizes))
	}

	type resource struct {
		path    string
		machine uint16
		data    []byte
	}
	resources := []resource{
		{
			path:    filepath.Join("cmd", "cyberagent-desktop", "traverse_board_windows_amd64.syso"),
			machine: 0x8664,
		},
		{
			path:    filepath.Join("cmd", "cyberagent-desktop", "traverse_board_windows_arm64.syso"),
			machine: 0xaa64,
		},
	}
	for index := range resources {
		resources[index].data = readTestFile(t, filepath.Join(root, resources[index].path))
		if len(resources[index].data) < 2 || binary.LittleEndian.Uint16(resources[index].data[:2]) != resources[index].machine {
			t.Fatalf("%s has the wrong COFF machine", resources[index].path)
		}
	}

	for index, wantSize := range wantSizes {
		offset := 6 + index*16
		gotSize := int(ico[offset])
		if gotSize == 0 {
			gotSize = 256
		}
		if gotSize != wantSize || int(ico[offset+1]) != int(ico[offset]) {
			t.Fatalf("ICO entry %d dimensions = %dx%d, want %dx%d", index, gotSize, gotSize, wantSize, wantSize)
		}
		length := int(binary.LittleEndian.Uint32(ico[offset+8 : offset+12]))
		dataOffset := int(binary.LittleEndian.Uint32(ico[offset+12 : offset+16]))
		if length <= 0 || dataOffset < 6+16*count || dataOffset+length > len(ico) {
			t.Fatalf("ICO entry %d points outside the file", index)
		}
		pngBytes := ico[dataOffset : dataOffset+length]
		assertPNGDimensions(t, "TraverseBoard.ico", pngBytes, wantSize, true)
		for _, current := range resources {
			if !bytes.Contains(current.data, pngBytes) {
				t.Fatalf("%s does not embed ICO entry %d (%dpx)", current.path, index, wantSize)
			}
		}
	}
}

func TestMacOSIconContainsCompleteRetinaSlotSet(t *testing.T) {
	t.Parallel()
	root := filepath.Clean(filepath.Join("..", ".."))
	icns := readTestFile(t, filepath.Join(root, "packaging", "macos", "TraverseBoard.icns"))
	if len(icns) < 8 || string(icns[:4]) != "icns" || int(binary.BigEndian.Uint32(icns[4:8])) != len(icns) {
		t.Fatal("TraverseBoard.icns has an invalid header")
	}
	want := []struct {
		typeCode string
		size     int
	}{
		{"icp4", 16},
		{"icp5", 32},
		{"ic07", 128},
		{"ic08", 256},
		{"ic09", 512},
		{"ic10", 1024},
		{"ic11", 32},
		{"ic12", 64},
		{"ic13", 256},
		{"ic14", 512},
	}
	offset := 8
	if len(icns) < offset+8 || string(icns[offset:offset+4]) != "TOC " {
		t.Fatal("TraverseBoard.icns does not begin with a table of contents")
	}
	tocLength := int(binary.BigEndian.Uint32(icns[offset+4 : offset+8]))
	if tocLength != 8+8*len(want) || offset+tocLength > len(icns) {
		t.Fatalf("ICNS TOC length = %d, want %d", tocLength, 8+8*len(want))
	}
	offset += tocLength
	for index, expected := range want {
		if offset+8 > len(icns) {
			t.Fatalf("ICNS entry %d header is truncated", index)
		}
		typeCode := string(icns[offset : offset+4])
		length := int(binary.BigEndian.Uint32(icns[offset+4 : offset+8]))
		if typeCode != expected.typeCode || length <= 8 || offset+length > len(icns) {
			t.Fatalf("ICNS entry %d = %q/%d, want %q", index, typeCode, length, expected.typeCode)
		}
		assertPNGDimensions(t, "TraverseBoard.icns "+typeCode, icns[offset+8:offset+length], expected.size, true)
		offset += length
	}
	if offset != len(icns) {
		t.Fatalf("TraverseBoard.icns has %d trailing bytes", len(icns)-offset)
	}
}

func assertPNGDimensions(t *testing.T, name string, content []byte, size int, roundedAlpha bool) {
	t.Helper()
	config, err := png.DecodeConfig(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	if config.Width != size || config.Height != size {
		t.Fatalf("%s dimensions = %dx%d, want %dx%d", name, config.Width, config.Height, size, size)
	}
	image, err := png.Decode(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("decode pixels for %s: %v", name, err)
	}
	corner := color.NRGBAModel.Convert(image.At(0, 0)).(color.NRGBA)
	center := color.NRGBAModel.Convert(image.At(size/2, size/2)).(color.NRGBA)
	if roundedAlpha {
		if corner.A > 32 || center.A != 255 {
			t.Fatalf("%s rounded alpha = corner %d, center %d; want <=32/255", name, corner.A, center.A)
		}
		return
	}
	if corner.A != 255 || center.A != 255 {
		t.Fatalf("%s approved master must stay opaque", name)
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}
