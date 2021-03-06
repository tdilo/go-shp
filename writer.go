package shp

import (
	"encoding/binary"
	"io"
	"log"
	"math"
	"os"
	"reflect"
	"strconv"
)

// Writer is the type that is used to write a new shapefile.
type Writer struct {
	filename     string
	shp          *os.File
	shx          *os.File
	GeometryType ShapeType
	num          int32
	bbox         Box

	dbf             *os.File
	dbfFields       []Field
	dbfHeaderLength int16
	dbfRecordLength int16
}

// Create returns a point to new Writer and the first error that was
// encountered. In case an error occurred the returned Writer point will be nil
// This also creates a corresponding SHX file. It is important to use Close()
// when done because that method writes all the headers for each file (SHP, SHX
// and DBF).
func Create(filename string, t ShapeType) (*Writer, error) {
	filename = filename[0 : len(filename)-3]
	shp, err := os.Create(filename + "shp")
	if err != nil {
		return nil, err
	}
	shx, err := os.Create(filename + "shx")
	if err != nil {
		return nil, err
	}
	shp.Seek(100, io.SeekStart)
	shx.Seek(100, io.SeekStart)
	w := &Writer{
		filename:     filename,
		shp:          shp,
		shx:          shx,
		GeometryType: t,
	}
	return w, nil
}

// Write shape to the Shapefile. This also creates
// a record in the SHX file and DBF file (if it is
// initialized). Returns the index of the written object
// which can be used in WriteAttribute.
func (w *Writer) Write(shape Shape) int32 {
	// increate bbox
	if w.num == 0 {
		w.bbox = shape.BBox()
	} else {
		w.bbox.Extend(shape.BBox())
	}

	w.num++
	binary.Write(w.shp, binary.BigEndian, w.num)
	w.shp.Seek(4, io.SeekCurrent)
	start, _ := w.shp.Seek(0, io.SeekCurrent)
	binary.Write(w.shp, binary.LittleEndian, w.GeometryType)
	shape.write(w.shp)
	finish, _ := w.shp.Seek(0, io.SeekCurrent)
	length := int32(math.Floor((float64(finish) - float64(start)) / 2.0))
	w.shp.Seek(start-4, io.SeekStart)
	binary.Write(w.shp, binary.BigEndian, length)
	w.shp.Seek(finish, io.SeekStart)

	// write shx
	binary.Write(w.shx, binary.BigEndian, int32((start-8)/2))
	binary.Write(w.shx, binary.BigEndian, length)

	// write empty record to dbf
	if w.dbf != nil {
		w.writeEmptyRecord()
	}

	return w.num - 1
}

// Close closes the Writer. This must be used at the end of
// the transaction because it writes the correct headers
// to the SHP/SHX and DBF files before closing.
func (w *Writer) Close() {
	w.writeHeader(w.shx)
	w.writeHeader(w.shp)
	w.shp.Close()
	w.shx.Close()

	if w.dbf == nil {
		w.SetFields([]Field{})
	}
	w.writeDbfHeader(w.dbf)
	w.dbf.Close()
}

// writeHeader wrires SHP/SHX headers to ws.
func (w *Writer) writeHeader(ws io.WriteSeeker) {
	filelength, _ := ws.Seek(0, io.SeekEnd)
	if filelength == 0 {
		filelength = 100
	}
	ws.Seek(0, io.SeekStart)
	// file code
	binary.Write(ws, binary.BigEndian, []int32{9994, 0, 0, 0, 0, 0})
	// file length
	binary.Write(ws, binary.BigEndian, int32(filelength/2))
	// version and shape type
	binary.Write(ws, binary.LittleEndian, []int32{1000, int32(w.GeometryType)})
	// bounding box
	binary.Write(ws, binary.LittleEndian, w.bbox)
	// elevation, measure
	binary.Write(ws, binary.LittleEndian, []float64{0.0, 0.0, 0.0, 0.0})
}

// writeDbfHeader writes a DBF header to ws.
func (w *Writer) writeDbfHeader(ws io.WriteSeeker) {
	ws.Seek(0, 0)
	// version, year (YEAR-1990), month, day
	binary.Write(ws, binary.LittleEndian, []byte{3, 24, 5, 3})
	// number of records
	binary.Write(ws, binary.LittleEndian, w.num)
	// header length, record length
	binary.Write(ws, binary.LittleEndian, []int16{w.dbfHeaderLength, w.dbfRecordLength})
	// padding
	binary.Write(ws, binary.LittleEndian, make([]byte, 20))

	for _, field := range w.dbfFields {
		binary.Write(ws, binary.LittleEndian, field)
	}

	// end with return
	ws.Write([]byte("\r"))
}

// SetFields sets field values in the DBF. This initializes the DBF file and
// should be used prior to writing any attributes.
func (w *Writer) SetFields(fields []Field) {
	if w.dbf != nil {
		log.Fatal("Cannot set fields in existing dbf")
	}

	var err error
	w.dbf, err = os.Create(w.filename + "dbf")
	if err != nil {
		log.Fatal("Failed to open " + w.filename + ".dbf")
	}
	w.dbfFields = fields

	// calculate record length
	w.dbfRecordLength = int16(1)
	for _, field := range w.dbfFields {
		w.dbfRecordLength += int16(field.Size)
	}

	// header lengh
	w.dbfHeaderLength = int16(len(w.dbfFields)*32 + 33)

	// fill header space with empty bytes for now
	buf := make([]byte, w.dbfHeaderLength)
	binary.Write(w.dbf, binary.LittleEndian, buf)

	// write empty records
	for n := int32(0); n < w.num; n++ {
		w.writeEmptyRecord()
	}
}

// Writes an empty record to the end of the DBF. This
// works by seeking to the end of the file and writing
// dbfRecordLength number of bytes. The first byte is a
// space that indicates a new record.
func (w *Writer) writeEmptyRecord() {
	w.dbf.Seek(0, io.SeekEnd)
	buf := make([]byte, w.dbfRecordLength)
	buf[0] = ' '
	binary.Write(w.dbf, binary.LittleEndian, buf)
}

// WriteAttribute writes value for field into the given row in the DBF. Row
// number should be the same as the order the Shape was written to the
// Shapefile. The field value corresponds the the field in the splice used in
// SetFields.
func (w *Writer) WriteAttribute(row int, field int, value interface{}) {
	var buf []byte
	switch reflect.TypeOf(value).Kind() {
	case reflect.Int:
		buf = []byte(strconv.Itoa(value.(int)))
	case reflect.Float64:
		precision := w.dbfFields[field].Precision
		buf = []byte(strconv.FormatFloat(value.(float64), 'f', int(precision), 64))
	case reflect.String:
		buf = []byte(value.(string))
	default:
		log.Fatal("Unsupported value type:", reflect.TypeOf(value))
	}

	if w.dbf == nil {
		log.Fatal("Initialize DBF by using SetFields first")
	}

	seekTo := 1 + int64(w.dbfHeaderLength) + (int64(row) * int64(w.dbfRecordLength))
	for n := 0; n < field; n++ {
		seekTo += int64(w.dbfFields[n].Size)
	}
	w.dbf.Seek(seekTo, io.SeekStart)
	binary.Write(w.dbf, binary.LittleEndian, buf)
}

// BBox returns the bounding box of the Writer.
func (w *Writer) BBox() Box {
	return w.bbox
}
