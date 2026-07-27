package sniff

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"testing"
)

// pdfHeader is the "%PDF-" marker plus the binary comment a real producer
// writes after it.
const pdfHeader = "%PDF-1.7\n%\xe2\xe3\xcf\xd3\n"

const (
	pngHeader  = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	jpegHeader = "\xff\xd8\xff\xe0\x00\x10JFIF\x00"
	gifHeader  = "GIF89a\x01\x00\x01\x00"
	// TIFF comes in both byte orders and detection must take either.
	tiffLeHeader = "II*\x00\x08\x00\x00\x00"
	tiffBeHeader = "MM\x00*\x00\x00\x00\x08"
	webpHeader   = "RIFF\x24\x00\x00\x00WEBPVP8 "
	avifHeader   = "\x00\x00\x00\x1cftypavif\x00\x00\x00\x00avifmif1miaf"
	heicHeader   = "\x00\x00\x00\x18ftypheic\x00\x00\x00\x00heicmif1"
)

// bmpHeader is built rather than written out because detection reads the DIB
// header size at offset 14 and only accepts the handful of documented values.
func bmpHeader() []byte {
	const fileHeaderSize, dibHeaderSize = 14, 40
	b := make([]byte, fileHeaderSize+dibHeaderSize)
	copy(b, "BM")
	binary.LittleEndian.PutUint32(b[2:], uint32(len(b)))
	binary.LittleEndian.PutUint32(b[10:], uint32(len(b)))
	binary.LittleEndian.PutUint32(b[14:], dibHeaderSize)
	binary.LittleEndian.PutUint32(b[18:], 1)
	binary.LittleEndian.PutUint32(b[22:], 1)
	return b
}

// zipEntryXml stands in for whatever XML part a real package would hold; only
// the entry names carry the format, so the contents can be anything.
var zipEntryXml = []byte(`<?xml version="1.0"?><d/>`)

// ooxmlFixture builds the smallest zip detection accepts as an OOXML document:
// the first entry has to be one of the package parts an Office writer emits,
// and a later entry names the payload directory that says which of docx, xlsx
// and pptx it is.
func ooxmlFixture(t *testing.T, payloadDir string, padding int) []byte {
	t.Helper()

	var buf bytes.Buffer
	archive := zip.NewWriter(&buf)
	// Stored rather than deflated so padding really occupies the bytes it
	// claims; a compressible filler would collapse and move nothing.
	writeZipEntry(t, archive, "[Content_Types].xml", zip.Store, make([]byte, padding))
	writeZipEntry(t, archive, "_rels/.rels", zip.Deflate, zipEntryXml)
	writeZipEntry(t, archive, payloadDir+"/document.xml", zip.Deflate, zipEntryXml)
	if err := archive.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}
	return buf.Bytes()
}

// odfFixture builds an OpenDocument package: an uncompressed "mimetype" entry
// holding the format's media type, which is what detection reads.
func odfFixture(t *testing.T, mediaType string) []byte {
	t.Helper()

	var buf bytes.Buffer
	archive := zip.NewWriter(&buf)
	writeZipEntry(t, archive, "mimetype", zip.Store, []byte(mediaType))
	writeZipEntry(t, archive, "content.xml", zip.Deflate, zipEntryXml)
	if err := archive.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}
	return buf.Bytes()
}

func writeZipEntry(t *testing.T, archive *zip.Writer, name string, method uint16, content []byte) {
	t.Helper()

	w, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: method})
	if err != nil {
		t.Fatalf("creating zip entry %q: %v", name, err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatalf("writing zip entry %q: %v", name, err)
	}
}

// Compound File Binary layout constants, as far as detection walks it: a
// 512-byte header, then sectors of the size the header declares.
const (
	cfbSectorSize    = 512
	cfbDirEntrySize  = 128
	cfbDirTypeRoot   = 5
	cfbDirTypeStream = 2
	cfbFreeSector    = -1
	cfbEndOfChain    = -2
	cfbSatSector     = -3
)

// cfbFixture builds the legacy Office container: the D0CF11E0 magic alone is
// not enough, because detection tells doc from xls from ppt by walking the
// directory for the stream each one names. Three sectors is all that takes —
// header, sector allocation table, directory — so the format that would
// otherwise need a checked-in binary is built here instead.
//
// streamName is the entry that names the format ("WordDocument", "Workbook",
// "PowerPoint Document"); one detection does not know leaves the file as the
// bare compound-file type.
func cfbFixture(streamName string) []byte {
	header := make([]byte, cfbSectorSize)
	binary.LittleEndian.PutUint64(header, 0xE11AB1A1E011CFD0)
	binary.LittleEndian.PutUint16(header[30:], 9) // sector size, as a power of two
	binary.LittleEndian.PutUint16(header[32:], 6) // short sector size, as a power of two
	putCfbSectorId(header, 44, 1)                 // one sector holds the allocation table
	putCfbSectorId(header, 48, 1)                 // the directory lives in sector 1
	binary.LittleEndian.PutUint32(header[56:], 4096)
	putCfbSectorId(header, 60, cfbEndOfChain) // no short-sector allocation table
	putCfbSectorId(header, 68, cfbEndOfChain) // no master allocation table extension
	binary.LittleEndian.PutUint32(header[72:], 0)
	// The header carries the first 109 allocation-table sector ids inline.
	putCfbSectorId(header, 76, 0)
	for i := 1; i < 109; i++ {
		putCfbSectorId(header, 76+4*i, cfbFreeSector)
	}

	sat := make([]byte, cfbSectorSize)
	putCfbSectorId(sat, 0, cfbSatSector)
	putCfbSectorId(sat, 4, cfbEndOfChain)
	for i := 2; i < cfbSectorSize/4; i++ {
		putCfbSectorId(sat, 4*i, cfbFreeSector)
	}

	dir := make([]byte, cfbSectorSize)
	writeCfbDirEntry(dir, "Root Entry", cfbDirTypeRoot)
	writeCfbDirEntry(dir[cfbDirEntrySize:], streamName, cfbDirTypeStream)

	return append(append(header, sat...), dir...)
}

// writeCfbDirEntry fills one directory record: a UTF-16LE name, its length in
// bytes including the terminator, and the entry type.
func writeCfbDirEntry(entry []byte, name string, entryType byte) {
	for i, r := range name {
		binary.LittleEndian.PutUint16(entry[2*i:], uint16(r))
	}
	binary.LittleEndian.PutUint16(entry[64:], uint16(2*(len(name)+1)))
	entry[66] = entryType
	putCfbSectorId(entry, 116, cfbEndOfChain)
	binary.LittleEndian.PutUint32(entry[120:], 0)
}

// putCfbSectorId writes a sector id, which is signed so the format's
// end-of-chain and free-sector sentinels fit alongside real sector numbers.
func putCfbSectorId(b []byte, offset int, id int32) {
	binary.LittleEndian.PutUint32(b[offset:], uint32(id))
}
